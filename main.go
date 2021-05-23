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
	"encoding/xml"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

var systemFields []string
var systemFieldsAdditional []string

var roomFields []string
var roomFieldsTemperature []string
var roomSetFields []string

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
	Topic               string
	FullInformation     bool
	LastNumberOfDevices int
}

func setupCloseHandler(bridge *bridgeCfg) {
	c := make(chan os.Signal)
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
		log.Println(err)
		return c
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println(err)
		return c
	}

	err = xml.Unmarshal(body, &c)
	if err != nil {
		log.Println(err)
		log.Println(string(body))
		return c
	}

	return c
}

func propagate(bridge *bridgeCfg, name string, value string, prefix string) bool {
	if stringSuffixInSlice(name, roomFieldsTemperature) {
		value = strings.Replace(value, ".", "", -1)
	}

	data := prefix + "." + name + "=" + url.QueryEscape(value)
	url := "http://" + bridge.HeatingURL + "/cgi-bin/writeVal.cgi?" + data

	log.Println("Propagate:", data)
	resp, err := http.Get(url)
	if err == nil {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)
		bridge.RefreshRoomChannel <- prefix + "."
		return string(body) == value
	}

	log.Println(err)
	return false
}

func publish(bridge *bridgeCfg, topic string, value string) {
	token := bridge.Client.Publish(topic, 0, false, value)
	token.Wait()
	if token.Error() != nil {
		log.Println(token.Error())
	}
}

func refreshSystemInformation(bridge *bridgeCfg) int {
	fields := systemFields
	if bridge.FullInformation {
		fields = append(fields, systemFieldsAdditional...)
	}

	c := fetch(bridge.HeatingURL, fields, "")

	totalNumberOfDevices := 0
	for i := 0; i < len(c.Entries); i++ {
		if c.Entries[i].Value == "" {
			continue
		}

		if c.Entries[i].Name == "totalNumberOfDevices" {
			v, err := strconv.Atoi(c.Entries[i].Value)
			if err == nil {
				totalNumberOfDevices = v
			}
		}

		name := strings.Replace(c.Entries[i].Name, ".", "/", -1)
		t := fmt.Sprint(bridge.Topic, "/", name)
		publish(bridge, t, c.Entries[i].Value)
	}

	return totalNumberOfDevices
}

func fetchTemperature(name string, value string) string {
	if stringSuffixInSlice(name, roomFieldsTemperature) && len(value) > 2 {
		value = value[:2] + "." + value[2:]
	}

	return value
}

func refreshRoomInformation(bridge *bridgeCfg, number string) {
	c := fetch(bridge.HeatingURL, roomFields, number)

	for i := 0; i < len(c.Entries); i++ {
		name := strings.Replace(c.Entries[i].Name, ".", "/", -1)
		t := fmt.Sprint(bridge.Topic, "/devices/", name)
		value := fetchTemperature(name, c.Entries[i].Value)
		publish(bridge, t, value)
	}
}

func refresh(bridge *bridgeCfg) {
	totalNumberOfDevices := refreshSystemInformation(bridge)

	if totalNumberOfDevices > bridge.LastNumberOfDevices {
		firstNewDevice := totalNumberOfDevices - (totalNumberOfDevices - bridge.LastNumberOfDevices)
		for i := firstNewDevice; i < totalNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			log.Println("Add room:", prefix)
			for _, name := range roomSetFields {
				topic := fmt.Sprint(bridge.Topic, "/devices/", prefix, "/set/", name)
				listen(bridge, topic)
			}
		}

		bridge.LastNumberOfDevices = totalNumberOfDevices

	} else if totalNumberOfDevices < bridge.LastNumberOfDevices {
		for i := totalNumberOfDevices; i < bridge.LastNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			log.Println("Remove room:", prefix)
			for _, name := range roomSetFields {
				topic := fmt.Sprint(bridge.Topic, "/devices/", prefix, "/set/", name)
				bridge.Client.Unsubscribe(topic)
			}
		}

		bridge.LastNumberOfDevices = totalNumberOfDevices
	}

	for i := 0; i < totalNumberOfDevices; i++ {
		room := fmt.Sprint("G", i, ".")
		bridge.RefreshRoomChannel <- room
	}
}

func listen(bridge *bridgeCfg, topic string) {
	bridge.Client.Subscribe(topic, 0, func(client MQTT.Client, msg MQTT.Message) {
		payload := string(msg.Payload())
		splitted := strings.SplitN(msg.Topic(), "/", -1)
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
	log.Println("Running...")
	refresh(bridge)
	ticker := time.NewTicker(time.Duration(bridge.Polling) * time.Second)

	go func() {
		for {
			select {
			case <-bridge.KeepRunning:
				return
			case <-ticker.C:
				refresh(bridge)
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
				refreshRoomInformation(bridge, room)
			}
		}
	}()
}

func attemptHandler(broker *url.URL, tlsCfg *tls.Config) *tls.Config {
	log.Println("Connecting...", broker)
	return tlsCfg
}

func connectHandler(client MQTT.Client) {
	log.Println("Connected")
}

func connectLostHandler(client MQTT.Client, err error) {
	log.Println("Connection lost", err)
}

func createClientOptions(broker string, user string, password string, cleansess bool) *MQTT.ClientOptions {
	rand.Seed(time.Now().UTC().UnixNano())
	id := fmt.Sprint("HeatingMqttBridge-", rand.Intn(1000))
	opts := MQTT.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(id)
	opts.SetUsername(user)
	opts.SetPassword(password)
	opts.SetCleanSession(cleansess)
	opts.SetConnectionAttemptHandler(attemptHandler)
	opts.SetOnConnectHandler(connectHandler)
	opts.SetConnectionLostHandler(connectLostHandler)
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
		log.Println(envName, "is undefined")
		os.Exit(1)
	}
}

func setBoolParam(value *bool, name string) {
	if !isFlagPassed(name) {
		v, err := strconv.ParseBool(os.Getenv(strings.ToUpper(name)))
		if err == nil {
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
	polling := flag.Int("polling", 90, "Refresh interval in seconds")
	full := flag.Bool("full", false, "Provide full information to broker")
	flag.Parse()

	setStringParam(heating, "HEATING", *env, "", true)
	setStringParam(topic, "TOPIC", *env, "roth", true)
	setStringParam(broker, "BROKER", *env, "", true)
	setStringParam(password, "BROKER_USER", *env, "", false)
	setStringParam(user, "BROKER_PSW", *env, "", false)

	if *env {
		setBoolParam(clean, "clean")
		setBoolParam(full, "full")

		if !isFlagPassed("polling") {
			v, err := strconv.Atoi(os.Getenv("POLLING"))
			if err == nil {
				*polling = v
			}
		}
	}

	if *polling < 0 {
		*polling = 90
	}

	return &bridgeCfg{
		Client:              MQTT.NewClient(createClientOptions(*broker, *user, *password, *clean)),
		KeepRunning:         make(chan bool),
		WriteChannel:        make(chan writeEvent, 50),
		RefreshRoomChannel:  make(chan string, 50),
		HeatingURL:          *heating,
		Polling:             *polling,
		Topic:               *topic,
		FullInformation:     *full,
		LastNumberOfDevices: 0,
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

	roomSetFields = []string{"name", "OPMode", "SollTemp"}
}

func main() {
	bridge := createBridge()
	setupCloseHandler(bridge)

	if token := bridge.Client.Connect(); token.Wait() && token.Error() != nil {
		log.Println(token.Error())
	} else {
		setFields()
		go running(bridge)
		<-bridge.KeepRunning
	}
}
