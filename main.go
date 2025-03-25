/*
	Copyright (c) 2021 A. Klitzing <aklitzing@gmail.com>

	Permission is hereby granted, free of charge, to any person obtaining
	a copy of this software and associated documentation files (the
	"Software"), to deal in the Software without restriction, including
	without limitation the rights to use, copy, modify, merge, publish,
	distribute, sublicense, and/or sell copies of the Software, and to
	permit persons to whom the Software is furnished to do so, subject to
	the following conditions:

	The above copyright notice and this permission notice shall be
	included in all copies or substantial portions of the Software.

	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
	EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
	MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
	NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
	LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
	OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
	WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	DNS "github.com/ncruces/go-dns"
)

var bridge *bridgeCfg

var lastTempChange = make(map[string]tempChange)

var systemFields []string
var systemFieldsAdditional []string

var roomFields []string
var roomFieldsTemperature []string
var roomSetFields []string

type tempChange struct {
	Temp    string
	Time    time.Time
	MinTemp float64
	MaxTemp float64
}

type jsonClimateDiscoveryDevice struct {
	Identifier string     `json:"identifiers"`
	Name       string     `json:"name"`
	Cns        [][]string `json:"cns"`
}

type jsonClimateAvailability struct {
	Topic string `json:"topic"`
	//Avail    string `json:"pl_avail"`
	//NotAvail string `json:"pl_not_avail"`
}

type jsonClimateDiscovery struct {
	Name      string                     `json:"name"`
	ModeCmdT  string                     `json:"mode_cmd_t"`
	ModeStatT string                     `json:"mode_stat_t"`
	Avty      []jsonClimateAvailability  `json:"avty"`
	AvtyMode  string                     `json:"avty_mode"`
	TempCmdT  string                     `json:"temp_cmd_t"`
	TempStatT string                     `json:"temp_stat_t"`
	CurrTempT string                     `json:"curr_temp_t"`
	TempUnit  string                     `json:"temp_unit"`
	MinTemp   string                     `json:"min_temp"`
	MaxTemp   string                     `json:"max_temp"`
	TempStep  string                     `json:"temp_step"`
	Modes     []string                   `json:"modes"`
	Device    jsonClimateDiscoveryDevice `json:"device"`
	UniqueID  string                     `json:"unique_id"`
}

type jsonSensorDiscovery struct {
	Name              string                     `json:"name"`
	Avty              []jsonClimateAvailability  `json:"avty"`
	AvtyMode          string                     `json:"avty_mode"`
	StateTopic        string                     `json:"stat_t"`
	UnitOfMeasurement string                     `json:"unit_of_meas"`
	StateClass        string                     `json:"stat_cla"`
	DeviceClass       string                     `json:"dev_cla"`
	Device            jsonClimateDiscoveryDevice `json:"device"`
	UniqueID          string                     `json:"unique_id"`
}

type content struct {
	XMLName xml.Name       `xml:"content"`
	Entries []contentValue `xml:"field"`
}

type contentValue struct {
	XMLName xml.Name `xml:"field"`
	Name    string   `xml:"n"`
	Value   string   `xml:"v"`
}

type writeEvent struct {
	Prefix string
	Name   string
	Value  string
}

type bridgeCfg struct {
	KeepRunning         chan bool
	Client              MQTT.Client
	WriteChannel        chan writeEvent
	RefreshRoomChannel  chan string
	HeatingURL          string
	Polling             int
	TempChange          int
	Topic               string
	Sensor              bool
	FullInformation     bool
	LastNumberOfDevices int
	SystemInformation   map[string]string
}

func identifier(bridge *bridgeCfg) string {
	return bridge.SystemInformation["hw.HostName"]
}

func setupCloseHandler(bridge *bridgeCfg) {
	c := make(chan os.Signal, 1) // we need to reserve to buffer size 1, so the notifier are not blocked
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		bridge.KeepRunning <- false
	}()
}

func stringSuffixInSlice(value string, list []string) bool {
	for _, entry := range list {
		if strings.HasSuffix(value, entry) {
			return true
		}
	}
	return false
}

func generateXML(values []string, prefix string) string {
	xmlValue := "<content>"
	for _, v := range values {
		xmlValue += "<field>"

		xmlValue += "<n>"
		xmlValue += prefix
		xmlValue += v
		xmlValue += "</n>"

		xmlValue += "</field>"
	}
	xmlValue += "</content>"

	return xmlValue
}

func fetch(ip string, values []string, prefix string) content {
	url := "http://" + ip + "/cgi-bin/ILRReadValues.cgi"
	xmlValue := generateXML(values, prefix)
	var c content

	resp, err := http.Post(url, "text/xml", bytes.NewBuffer([]byte(xmlValue)))
	if err != nil {
		log.Error().Err(err).Msg("Cannot fetch data")
		return c
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Bytes("body", body).Msg("Cannot read body")
		return c
	}

	err = xml.Unmarshal(body, &c)
	if err != nil {
		log.Error().Err(err).Bytes("body", body).Msg("Cannot parse body")
		return c
	}

	return c
}

func checkTemperatureSanity(prefix string, value string) bool {
	lastChange := lastTempChange[prefix]

	if userValue, err := strconv.ParseFloat(value, 64); err == nil {
		return userValue <= lastChange.MaxTemp && userValue >= lastChange.MinTemp
	}

	log.Error().Str("value", value).Float64("minTemp", lastChange.MinTemp).Float64("maxTemp", lastChange.MaxTemp).Msg("Cannot convert compare values")
	return false
}

func propagate(bridge *bridgeCfg, name string, value string, prefix string) bool {
	if stringSuffixInSlice(name, roomFieldsTemperature) {
		if !checkTemperatureSanity(prefix, value) {
			log.Error().Str("value", value).Msg("Propagate canceled | Value is not valid")
			return false
		}

		if len(value) > 5 {
			value = value[:5] // cut off 20.123456789
		}

		value = strings.ReplaceAll(value, ".", "")
		for i := len(value); i < 4; i++ {
			value += "0"
		}
	} else if stringSuffixInSlice(name, []string{"OPMode"}) {
		if strings.EqualFold(value, "heat") || strings.EqualFold(value, "on") {
			value = "0"
		} else if strings.EqualFold(value, "off") {
			value = "2"
		}
	} else if stringSuffixInSlice(name, []string{"TempSIUnit"}) {
		if strings.EqualFold(value, "C") {
			value = "0"
		} else if strings.EqualFold(value, "F") {
			value = "1"
		}
	}

	data := prefix + "." + name + "=" + url.QueryEscape(value)
	url := "http://" + bridge.HeatingURL + "/cgi-bin/writeVal.cgi?" + data

	log.Info().Str("data", data).Msg("Propagate")
	resp, err := http.Get(url)
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		bridge.RefreshRoomChannel <- prefix
		return string(body) == value
	}

	log.Error().Err(err).Msg("Propagate failed")
	return false
}

func checkLastTempChange(bridge *bridgeCfg, number string, value string,
	sollTempMin string, sollTempMax string) {
	prefix := bridge.Topic + "/" + number
	deferedState := "online"
	defer func(state *string) {
		publish(bridge, prefix+"/available", *state, true)
	}(&deferedState)

	lastChange := lastTempChange[number]
	if lastChange.Temp == value {
		maxLastChangeTime := lastChange.Time.Add(time.Hour * time.Duration(bridge.TempChange))
		if time.Now().After(maxLastChangeTime) {
			log.Info().Str("room", number).Msg("No temperature change")
			deferedState = "offline"
			publish(bridge, prefix+"/RaumTempLastChange", lastChange.Time.String(), false)
		}
		return
	}

	minTemp, _ := strconv.ParseFloat(sollTempMin, 64)
	maxTemp, _ := strconv.ParseFloat(sollTempMax, 64)
	lastTempChange[number] = tempChange{
		Temp:    value,
		Time:    time.Now(),
		MinTemp: minTemp,
		MaxTemp: maxTemp,
	}
}

func publish(bridge *bridgeCfg, topic string, value string, retained bool) {
	token := bridge.Client.Publish(topic, 0, retained, value)
	token.Wait()
	if token.Error() != nil {
		log.Error().Err(token.Error()).Str("value", value).Str("topic", topic).Msg("Cannot publish value")
	}
}

func publishJSON(bridge *bridgeCfg, number string, name string, siUnit string,
	sollTempMin string, sollTempMax string) {
	id := identifier(bridge)
	prefix := bridge.Topic + "/" + number
	if siUnit == "0" {
		siUnit = "C"
	} else if siUnit == "1" {
		siUnit = "F"
	}

	mac := strings.ReplaceAll(bridge.SystemInformation["hw.Addr"], "-", ":")
	jsonDiscoveryDevice := jsonClimateDiscoveryDevice{
		Identifier: id,
		Name:       id,
		Cns:        [][]string{{"mac", mac}},
	}

	jsonAvailability := []jsonClimateAvailability{
		{
			Topic: prefix + "/available",
			//Avail:    "online",
			//NotAvail: "offline",
		},
		{
			Topic: bridge.Topic + "/available",
			//Avail:    "online",
			//NotAvail: "offline",
		}}

	jsonDiscoveryClimate := jsonClimateDiscovery{
		Name:      name,
		Avty:      jsonAvailability,
		AvtyMode:  "all",
		UniqueID:  id + "-" + number,
		Device:    jsonDiscoveryDevice,
		ModeCmdT:  prefix + "/set/OPMode",
		ModeStatT: prefix + "/OPMode_mode",
		TempCmdT:  prefix + "/set/SollTemp",
		TempStatT: prefix + "/SollTemp",
		CurrTempT: prefix + "/RaumTemp",
		TempUnit:  siUnit,
		MinTemp:   sollTempMin,
		MaxTemp:   sollTempMax,
		TempStep:  "0.5",
		Modes:     []string{"off", "heat"},
	}

	climateValueJSON, err := json.Marshal(jsonDiscoveryClimate)
	if err != nil {
		log.Error().Err(err).Msg("Cannot marshal climate discovery")
		return
	}

	climateTopic := "homeassistant/climate/" + id + "/" + number + "/config"
	publish(bridge, climateTopic, string(climateValueJSON), false)

	if bridge.Sensor {
		jsonDiscoverySensor := jsonSensorDiscovery{
			Name:              name,
			Avty:              jsonAvailability,
			AvtyMode:          "all",
			UniqueID:          id + "-sensor-" + number,
			Device:            jsonDiscoveryDevice,
			StateTopic:        prefix + "/RaumTemp",
			UnitOfMeasurement: "°" + siUnit,
			StateClass:        "measurement",
			DeviceClass:       "temperature",
		}

		sensorValueJSON, err := json.Marshal(jsonDiscoverySensor)
		if err != nil {
			log.Error().Err(err).Msg("Cannot marshal sensor discovery")
			return
		}

		sensorTopic := "homeassistant/sensor/" + id + "/" + number + "/config"
		publish(bridge, sensorTopic, string(sensorValueJSON), false)
	}
}

func refreshSystemInformation(bridge *bridgeCfg) int {
	fields := systemFields
	if bridge.FullInformation {
		fields = append(fields, systemFieldsAdditional...)
	}

	c := fetch(bridge.HeatingURL, fields, "")

	totalNumberOfDevices := 0
	bridge.SystemInformation = map[string]string{}
	for i := 0; i < len(c.Entries); i++ {
		if c.Entries[i].Value == "" {
			continue
		}

		if c.Entries[i].Name == "totalNumberOfDevices" {
			if v, err := strconv.Atoi(c.Entries[i].Value); err == nil {
				totalNumberOfDevices = v
			}
		}

		bridge.SystemInformation[c.Entries[i].Name] = c.Entries[i].Value

		name := strings.ReplaceAll(c.Entries[i].Name, ".", "/")
		t := fmt.Sprint(bridge.Topic, "/", name)
		publish(bridge, t, c.Entries[i].Value, false)
	}

	return totalNumberOfDevices
}

func fetchTemperature(name string, value string) string {
	if stringSuffixInSlice(name, roomFieldsTemperature) && len(value) > 2 {
		decimalPoint := len(value) - 2
		value = value[:decimalPoint] + "." + value[decimalPoint:]
	}

	return value
}

func refreshRoomInformation(bridge *bridgeCfg, number string) {
	c := fetch(bridge.HeatingURL, roomFields, number+".")

	name := number
	siUnit := "0"
	raumTemp := "0"
	sollTemp := "0"
	sollTempMin := "0"
	sollTempMax := "30"

	for i := 0; i < len(c.Entries); i++ {
		room := strings.ReplaceAll(c.Entries[i].Name, ".", "/")
		t := fmt.Sprint(bridge.Topic, "/", room)
		value := fetchTemperature(room, c.Entries[i].Value)
		publish(bridge, t, value, true)

		if strings.HasSuffix(room, "OPMode") {
			if value == "0" {
				value = "heat"
			} else if value == "2" {
				value = "off"
			}
			publish(bridge, t+"_mode", value, true)
		} else if strings.HasSuffix(room, "name") {
			name = value
		} else if strings.HasSuffix(room, "RaumTemp") {
			raumTemp = value
		} else if strings.HasSuffix(room, "SollTemp") {
			sollTemp = value
		} else if strings.HasSuffix(room, "TempSIUnit") {
			if value == "" {
				log.Warn().Msgf("TempSIUnit of %s is undefined. Use %s/set/%s", number, bridge.Topic, room)
			}
			siUnit = value
		} else if strings.HasSuffix(room, "SollTempMinVal") {
			sollTempMin = value
		} else if strings.HasSuffix(room, "SollTempMaxVal") {
			sollTempMax = value
		}
	}

	checkLastTempChange(bridge, number, raumTemp, sollTempMin, sollTempMax)
	publishJSON(bridge, number, name, siUnit, sollTempMin, sollTempMax)
	log.Debug().Str("name", name).Str("raumTemp", raumTemp).Str("sollTemp", sollTemp).Time("tempChange", lastTempChange[number].Time).Msg(number)
}

func refresh(bridge *bridgeCfg) {
	publish(bridge, bridge.Topic+"/available", "online", true)
	totalNumberOfDevices := refreshSystemInformation(bridge)

	if bridge.LastNumberOfDevices == -1 {
		bridge.LastNumberOfDevices = 0 // initialized!
		log.Info().Msgf("Host: %s", identifier(bridge))
		listenStateHA(bridge)
	}

	if totalNumberOfDevices > bridge.LastNumberOfDevices {
		firstNewDevice := totalNumberOfDevices - (totalNumberOfDevices - bridge.LastNumberOfDevices)
		for i := firstNewDevice; i < totalNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			log.Info().Msgf("Add room: %s", prefix)
			for _, name := range roomSetFields {
				topic := fmt.Sprint(bridge.Topic, "/", prefix, "/set/", name)
				listen(bridge, topic)
			}
		}

		bridge.LastNumberOfDevices = totalNumberOfDevices

	} else if totalNumberOfDevices < bridge.LastNumberOfDevices {
		for i := totalNumberOfDevices; i < bridge.LastNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			log.Info().Msgf("Remove room: %s", prefix)
			for _, name := range roomSetFields {
				topic := fmt.Sprint(bridge.Topic, "/", prefix, "/set/", name)
				bridge.Client.Unsubscribe(topic)
			}
		}

		bridge.LastNumberOfDevices = totalNumberOfDevices
	}

	for i := 0; i < totalNumberOfDevices; i++ {
		bridge.RefreshRoomChannel <- fmt.Sprint("G", i)
	}
}

func listenStateHA(bridge *bridgeCfg) {
	bridge.Client.Subscribe("homeassistant/status", 0, func(client MQTT.Client, msg MQTT.Message) {
		payload := string(msg.Payload())
		if payload == "online" {
			bridge.RefreshRoomChannel <- ""
		}
	})
}

func listen(bridge *bridgeCfg, topic string) {
	bridge.Client.Subscribe(topic, 0, func(client MQTT.Client, msg MQTT.Message) {
		payload := string(msg.Payload())
		splitted := strings.Split(msg.Topic(), "/")
		if len(splitted) > 3 {
			event := writeEvent{
				Prefix: splitted[len(splitted)-3],
				Name:   splitted[len(splitted)-1],
				Value:  payload,
			}
			bridge.WriteChannel <- event
		}
	})
}

func running(bridge *bridgeCfg) {
	log.Debug().Msg("Running...")
	ticker := time.NewTicker(time.Duration(bridge.Polling) * time.Second)

	go func() {
		for {
			select {
			case <-bridge.KeepRunning:
				return
			case <-ticker.C:
				bridge.RefreshRoomChannel <- ""
			}
		}
	}()

	go func() {
		for {
			select {
			case <-bridge.KeepRunning:
				return
			case event := <-bridge.WriteChannel:
				propagate(bridge, event.Name, event.Value, event.Prefix)
			}
		}
	}()

	go func() {
		for {
			select {
			case <-bridge.KeepRunning:
				return
			case room := <-bridge.RefreshRoomChannel:
				if room == "" {
					refresh(bridge)
				} else {
					refreshRoomInformation(bridge, room)
				}
			}
		}
	}()
}

func attemptHandler(broker *url.URL, tlsCfg *tls.Config) *tls.Config {
	log.Debug().Stringer("broker", broker).Msg("Connecting...")
	return tlsCfg
}

func connectHandler(client MQTT.Client) {
	log.Debug().Msg("Connected")
	// just reset if connection was lost
	bridge.LastNumberOfDevices = -1
	bridge.RefreshRoomChannel <- ""
}

func connectLostHandler(client MQTT.Client, err error) {
	log.Warn().Err(err).Msg("Connection lost")
}

func createClientOptions(broker string, user string, password string, cleansess bool, topic string) *MQTT.ClientOptions {
	opts := MQTT.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID("HeatingMqttBridge")
	opts.SetUsername(user)
	opts.SetPassword(password)
	opts.SetCleanSession(cleansess)
	opts.SetConnectionAttemptHandler(attemptHandler)
	opts.SetOnConnectHandler(connectHandler)
	opts.SetConnectionLostHandler(connectLostHandler)
	opts.SetWill(topic+"/available", "offline", 0, true)
	return opts
}

func setStringParam(param *string, envName string, useEnv bool, defaultValue string, required bool) {
	if *param == "" {
		if useEnv {
			*param = os.Getenv(envName)
		}

		if *param == "" {
			*param = defaultValue
		}
	}

	if required && *param == "" {
		log.Fatal().Msgf("%s is not defined", envName)
	}
}

func setBoolParam(value *bool, name string) {
	if !isFlagPassed(name) {
		if v, err := strconv.ParseBool(os.Getenv(strings.ToUpper(name))); err == nil {
			*value = v
		}
	}
}

func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func createBridge() *bridgeCfg {
	env := flag.Bool("env", false, "Allow environment variables if provided")
	heating := flag.String("heating", "", "The IP/hostname of the Roth EnergyLogic")
	topic := flag.String("topic", "", "The topic name to/from which to publish/subscribe")
	broker := flag.String("broker", "", "The broker URI. ex: tcp://10.10.1.1:1883")
	password := flag.String("password", "", "The password (optional)")
	user := flag.String("user", "", "The User (optional)")
	clean := flag.Bool("clean", false, "Set clean Session")
	polling := flag.Int("polling", 300, "Refresh interval in seconds")
	tempchange := flag.Int("tempchange", 12, "Temperature change warning in hours")
	full := flag.Bool("full", false, "Provide full information to broker")
	sensor := flag.Bool("sensor", true, "Send additional sensor entity")
	dnsCache := flag.Bool("dns", true, "Use internal DNS cache")
	verbose := flag.Bool("verbose", false, "Provide verbose log information")
	flag.Parse()

	setStringParam(heating, "HEATING", *env, "", true)
	setStringParam(topic, "TOPIC", *env, "roth", true)
	setStringParam(broker, "BROKER", *env, "", true)
	setStringParam(user, "BROKER_USER", *env, "", false)
	setStringParam(password, "BROKER_PSW", *env, "", false)

	if *env {
		setBoolParam(clean, "clean")
		setBoolParam(full, "full")
		setBoolParam(sensor, "sensor")
		setBoolParam(dnsCache, "dns")
		setBoolParam(verbose, "verbose")

		if !isFlagPassed("polling") {
			if v, err := strconv.Atoi(os.Getenv("POLLING")); err == nil {
				*polling = v
			}
		}

		if !isFlagPassed("tempchange") {
			if v, err := strconv.Atoi(os.Getenv("TEMPCHANGE")); err == nil {
				*tempchange = v
			}
		}
	}

	if *polling < 0 {
		*polling = 300
	}

	if *tempchange < 0 {
		*tempchange = 12
	}

	if *dnsCache {
		log.Debug().Msg("Use internal DNS cache")
		net.DefaultResolver = DNS.NewCachingResolver(net.DefaultResolver)
	}

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *verbose {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	return &bridgeCfg{
		Client:              MQTT.NewClient(createClientOptions(*broker, *user, *password, *clean, *topic)),
		KeepRunning:         make(chan bool),
		WriteChannel:        make(chan writeEvent, 50),
		RefreshRoomChannel:  make(chan string, 50),
		HeatingURL:          *heating,
		Polling:             *polling,
		TempChange:          *tempchange,
		Topic:               *topic,
		Sensor:              *sensor,
		FullInformation:     *full,
		LastNumberOfDevices: -1,
		SystemInformation:   make(map[string]string),
	}
}

func setFields() {
	// System information
	systemFields = []string{
		"isMaster", "totalNumberOfDevices", "numberOfSlaveControllers",
		"hw.HostName", "hw.IP", "hw.NM", "hw.GW", "hw.Addr", "hw.DNS1", "hw.DNS2",

		"R0.SystemStatus", "R0.DateTime",
		"R0.kurzID", "R0.numberOfPairedDevices",
		"R1.kurzID", "R1.numberOfPairedDevices",
		"R2.kurzID", "R2.numberOfPairedDevices",
	}

	systemFieldsAdditional = []string{
		"R0.Safety", "R0.Taupunkt", "R0.OutTemp", "R0.ErrorCode",
		"R0.WeekProgWarn", "R0.OPModeRegler", "R0.HeatCool", "R0.Alarm1",

		"R0.uniqueID", "R1.uniqueID", "R2.uniqueID",

		"STM-APP", "STM-BL",
		"STELL-APP", "STELL-BL",
		"VPI.href", "VPI.state",
		"CD.uname", "CD.upass", "CD.ureg",
	}

	// Room information
	roomFieldsTemperature = []string{"RaumTemp", "SollTemp",
		"SollTempStepVal", "SollTempMinVal", "SollTempMaxVal",
	}

	roomFields = []string{"name", "kurzID", "ownerKurzID", "OPMode", "OPModeEna",
		"TempSIUnit", "WeekProg", "WeekProgEna",
	}

	roomFields = append(roomFields, roomFieldsTemperature...)

	roomSetFields = []string{"name", "OPMode", "SollTemp", "TempSIUnit"}
}

func setLogger() {
	zerolog.TimeFieldFormat = time.DateTime
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: zerolog.TimeFieldFormat})
}

func main() {
	setLogger()
	bridge = createBridge()
	setupCloseHandler(bridge)

	if token := bridge.Client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal().Err(token.Error()).Msg("Cannot connect to broker")
	} else {
		setFields()
		go running(bridge)
		<-bridge.KeepRunning
	}
}
