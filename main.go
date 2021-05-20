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
	"encoding/xml"
	"flag"
	"fmt"
	MQTT "github.com/eclipse/paho.mqtt.golang"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
	HeatingUrl          string
	Polling             int
	Topic               string
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

func generateXml(values []string, prefix string) string {
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

func fetch(ip string, values []string, prefix string) []byte {
	url := "http://" + ip + "/cgi-bin/ILRReadValues.cgi"
	xmlValue := generateXml(values, prefix)

	resp, err := http.Post(url, "text/xml", bytes.NewBuffer([]byte(xmlValue)))
	if err == nil {
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err == nil {
			return body
		}
	}

	log.Fatal(err)
	return []byte("")
}

func propagate(bridge *bridgeCfg, name string, value string, prefix string) bool {
	url := "http://" + bridge.HeatingUrl + "/cgi-bin/writeVal.cgi?" + prefix + "." + name + "=" + value

	resp, err := http.Get(url)
	if err == nil {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)
		bridge.RefreshRoomChannel <- prefix + "."
		return string(body) == value
	}

	return false
}

func refreshSystemInformation(bridge *bridgeCfg) int {
	fields := []string{
		"isMaster", "totalNumberOfDevices", "numberOfSlaveControllers",

		"hw.HostName", "hw.IP", "hw.NM", "hw.GW", "hw.Addr", "hw.DNS1", "hw.DNS2",

		"R0.SystemStatus", "R0.DateTime", "R0.Safety", "R0.Taupunkt", "R0.OutTemp", "R0.ErrorCode",
		"R0.WeekProgWarn", "R0.OPModeRegler", "R0.HeatCool", "R0.Alarm1",

		"R0.kurzID", "R0.numberOfPairedDevices", "R0.uniqueID",
		"R1.kurzID", "R1.numberOfPairedDevices", "R1.uniqueID",
		"R2.kurzID", "R2.numberOfPairedDevices", "R2.uniqueID",

		"STM-APP", "STM-BL",
		"STELL-APP", "STELL-BL",
		"VPI.href", "VPI.state",
		"CD.uname", "CD.upass", "CD.ureg"}

	body := fetch(bridge.HeatingUrl, fields, "")

	var c content
	xml.Unmarshal(body, &c)

	totalNumberOfDevices := 0
	for i := 0; i < len(c.Entries); i++ {
		if c.Entries[i].Name == "totalNumberOfDevices" {
			v, err := strconv.Atoi(c.Entries[i].Value)
			if err == nil {
				totalNumberOfDevices = v
			}
		}

		t := fmt.Sprint(bridge.Topic, "/", c.Entries[i].Name)
		token := bridge.Client.Publish(t, 0, false, c.Entries[i].Value)
		token.Wait()
	}

	return totalNumberOfDevices
}

func refreshRoomInformation(bridge *bridgeCfg, number string) {

	fields := []string{"name", "kurzID", "ownerKurzID", "OPMode", "OPModeEna",
		"RaumTemp", "SollTemp", "TempSIUnit", "WeekProg", "WeekProgEna",
		"SollTempStepVal", "SollTempMinVal", "SollTempMaxVal"}

	body := fetch(bridge.HeatingUrl, fields, number)

	var c content
	xml.Unmarshal(body, &c)

	for i := 0; i < len(c.Entries); i++ {
		splitted := strings.SplitN(c.Entries[i].Name, ".", 2)
		t := fmt.Sprint(bridge.Topic, "/devices/", splitted[0], "/", splitted[1])
		token := bridge.Client.Publish(t, 0, false, c.Entries[i].Value)
		token.Wait()
	}
}

func refresh(bridge *bridgeCfg) {
	totalNumberOfDevices := refreshSystemInformation(bridge)

	setFields := []string{"name", "OPMode", "SollTemp"}

	if totalNumberOfDevices > bridge.LastNumberOfDevices {
		firstNewDevice := totalNumberOfDevices - (totalNumberOfDevices - bridge.LastNumberOfDevices)
		for i := firstNewDevice; i < totalNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			for _, name := range setFields {
				topic := fmt.Sprint(bridge.Topic, "/devices/", prefix, "/set/", name)
				listen(bridge, topic)
			}
		}

		bridge.LastNumberOfDevices = totalNumberOfDevices

	} else if totalNumberOfDevices < bridge.LastNumberOfDevices {
		for i := totalNumberOfDevices; i < bridge.LastNumberOfDevices; i++ {
			prefix := fmt.Sprint("G", i)
			for _, name := range setFields {
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
	fmt.Println("Running...")
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

func createClientOptions(broker string, user string, password string, cleansess bool) *MQTT.ClientOptions {
	rand.Seed(time.Now().UTC().UnixNano())
	id := fmt.Sprint("HeatingMqttBridge-", rand.Intn(1000))
	opts := MQTT.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(id)
	opts.SetUsername(user)
	opts.SetPassword(password)
	opts.SetCleanSession(cleansess)
	return opts
}

func main() {
	heating := flag.String("heating", "", "The IP/hostname of the Roth EnergyLogic")
	topic := flag.String("topic", "", "The topic name to/from which to publish/subscribe")
	broker := flag.String("broker", "", "The broker URI. ex: tcp://10.10.1.1:1883")
	password := flag.String("password", "", "The password (optional)")
	user := flag.String("user", "", "The User (optional)")
	cleansess := flag.Bool("clean", false, "Set Clean Session (default false)")
	polling := flag.Int("polling", 90, "Refresh interval in seconds (default 90 seconds)")
	flag.Parse()

	if *topic == "" {
		*topic = "roth"
	}

	if *polling < 0 {
		*polling = 90
	}

	if *broker == "" {
		fmt.Println("Broker is undefined")
		os.Exit(1)
	}

	if *heating == "" {
		fmt.Println("Heating is undefined")
		os.Exit(2)
	}

	bridge := &bridgeCfg{
		Client:              MQTT.NewClient(createClientOptions(*broker, *user, *password, *cleansess)),
		KeepRunning:         make(chan bool),
		WriteChannel:        make(chan writeEvent, 50),
		RefreshRoomChannel:  make(chan string, 50),
		HeatingUrl:          *heating,
		Polling:             *polling,
		Topic:               *topic,
		LastNumberOfDevices: 0,
	}
	setupCloseHandler(bridge)

	if token := bridge.Client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal(token.Error())
		bridge.KeepRunning <- false
	} else {
		go running(bridge)
	}

	<-bridge.KeepRunning
}
