package main

import (
	"log"
	"os"

	"github.com/sipb/mm2zephyr/mm"
	"github.com/sipb/mm2zephyr/zephyr"
)

func main() {

	bot, err := mm.New(os.Getenv("MM_AUTH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	client, err := zephyr.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	scriptsChan, err := client.SubscribeAndListen("scripts", "")
	if err != nil {
		log.Fatal(err)
	}
	for message := range scriptsChan {
		log.Printf("received message %#v", message)
		username := message.Header.Sender
		messageText := message.Body[1]
		err = bot.SendMessageToChannel("Scripts", messageText, username)
		if err != nil {
			log.Fatal(err)
		}
	}
}
