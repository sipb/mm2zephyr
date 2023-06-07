# mm2zephyr

SIPB's Mattermost-Zephyr bridge

## Implementing changes

1. Stop the service.

   ```bash
   systemctl stop mm2zephyr.service
   ```

1. Log in to the XVM and make a backup of `mm2zephyr`.

   ```bash
   cp /usr/local/bin/mm2zephyr mm2zephyr-backups/mm2zephyr_$(date +"%Y-%m-%d")
   ```

1. Switch to `quentin` (`su quentin`), who has all the `go` environment set up. Compile the updated repo.

   ```bash
   go build -v -o mm2zephyr cmd/mm2zephyr/main.go`
   ```

1. Copy `mm2zephyr` to `/usr/local/bin/mm2zephyr`.
1. Start the service.

   ```bash
   systemctl start mm2zephyr.service`
   ```

Note for future: If ever moving to a different system, you may need to install dependencies:

* `apt-get install golang`
* `apt-get install g++`
* `apt-get install libkrb5-dev`
