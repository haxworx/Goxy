#!/bin/sh
CONFIG_DIR="config"

mkdir $CONFIG_DIR

if [ -f "$CONFIG_DIR/server.key" ] && [ -f "$CONFIG_DIR/server.crt" ]; then
	echo "Keys already generated!";
else
	openssl genrsa -out "$CONFIG_DIR/server.key" 2048
	openssl req -new -x509 -sha256 -key "$CONFIG_DIR/server.key" -out "$CONFIG_DIR/server.crt" -days 3650
fi

exit 0;
