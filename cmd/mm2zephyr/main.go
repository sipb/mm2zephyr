package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"time"

	"github.com/sipb/mm2zephyr/bridge"
	"gopkg.in/yaml.v2"
)

var (
	configFile = flag.String("config", "config.yml", "Path to configuration file")
)

func main() {
	flag.Parse()

	ctx := context.Background()

	data, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	var config bridge.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatalf("unable to parse config: %v", err)
	}
	token := os.Getenv("MM_AUTH_TOKEN")
	b := bridge.New(config, token)
	for {
		err := b.Run(ctx)
		log.Printf("bridge failed: %v", err)
		time.Sleep(time.Second)
	}
}
