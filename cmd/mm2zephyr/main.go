package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sipb/mm2zephyr/bridge"
	"gopkg.in/yaml.v2"
)

var (
	configFile = flag.String("config", "config.yml", "Path to configuration file")
)

func main() {
	flag.Parse()

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// Listen for Ctrl-C
	ctx, cancel := context.WithCancel(context.Background())

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		<-c
		cancel()
	}()

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

	for ctx.Err() == nil {
		start := time.Now()
		err := b.Run(ctx)
		log.Printf("bridge failed: %v", err)
		if time.Since(start) < 5*time.Second {
			time.Sleep(time.Second)
		}
	}
}
