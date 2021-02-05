package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/sipb/mm2zephyr/mm"
	"github.com/sipb/mm2zephyr/zephyr"
	z "github.com/zephyr-im/zephyr-go"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Mappings []Mapping `yaml:"mappings"`
}

type Mapping struct {
	Channel  string `yaml:"channel"`
	Class    string `yaml:"class"`
	Instance string `yaml:"instance"`
}

var (
	configFile = flag.String("config", "config.yml", "Path to configuration file")
)

func main() {
	flag.Parse()

	bot, err := mm.New(os.Getenv("MM_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	client, err := zephyr.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	data, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatalf("unable to parse config: %v", err)
	}
	var eg errgroup.Group
	for _, mapping := range config.Mappings {
		// Make a local copy for the closure
		mapping := mapping
		instance := mapping.Instance
		if instance == "" {
			instance = "*"
		}
		ch, err := client.SubscribeAndListen(mapping.Class, instance)
		if err != nil {
			log.Fatal(err)
		}
		eg.Go(func() error {
			for message := range ch {
				logMessage(message)
				username := message.Header.Sender
				messageText := message.Body[1]

				if instance == "*" && message.Instance != "personal" {
					messageText = fmt.Sprintf("[-i %s] %s", message.Instance, messageText)
				}

				err = bot.SendMessageToChannel(mapping.Channel, messageText, username)
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		log.Fatal(err)
	}
}

func logMessage(message *z.Message) {
	body := message.Body[0]
	zsig := message.Header.Sender
	if len(message.Body) > 1 {
		body = message.Body[1]
		zsig = message.Body[0]
	}
	log.Printf("[-c %s -i %s] %s <%s>: %s", message.Class, message.Instance, zsig, message.Header.Sender, body)
}
