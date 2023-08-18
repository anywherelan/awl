#!/bin/bash

# Get latest version
version=$(wget -qO- -t1 -T2 "https://api.github.com/repos/anywherelan/awl/releases/latest" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')

# Get architecture
architecture=$(uname -m)
case $architecture in
    "arm64" | *aarch64* | *armv8*)
        architecture="arm64"
        ;;
    "armv7l" | "armv6l")
        architecture="arm"
        ;;
    "i386")
        architecture="386"
        ;;
    "x86_64")
        architecture="amd64"
        ;;
    "mips")
        architecture="mips"
        ;;
    "mipsle")
        architecture="mipsle"
        ;;
    *)
        echo "awl doesn't support your architecture"
        exit 1
        ;;
esac

# Check if the script is running as root
if [[ $EUID -ne 0 ]]; then
    echo "Please run this script as root!"
    exit 1
fi

# Let's go! Download the file
mkdir -p /etc/anywherelan
cd /etc/anywherelan
# NOTE: you need to set the latest release tag and correct arch (x86/arm/etc)
wget https://github.com/anywherelan/awl/releases/download/${version}/awl-linux-${architecture}-${version}.tar.gz
tar xfz awl-linux-${architecture}-${version}.tar.gz
ln -s /etc/anywherelan/awl /usr/bin/awl
rm awl-linux-${architecture}-${version}.tar.gz

# Create systemd file
cat << EOF > /etc/systemd/system/awl.service
[Unit]
Description=Anywherelan server
After=network-online.target nss-lookup.target
Wants=network-online.target nss-lookup.target
ConditionPathExists=/etc/anywherelan/awl

[Service]
Type=simple
Environment="AWL_DATA_DIR=/etc/anywherelan"
WorkingDirectory=/etc/anywherelan/
ExecStart=/etc/anywherelan/awl
Restart=always
RestartSec=5s
LimitNOFILE=4000

[Install]
WantedBy=multi-user.target
EOF

# Setup systemd unit
systemctl daemon-reload
systemctl enable awl.service
systemctl start awl.service
systemctl status awl.service

echo "Anywherelan was installed successfully to /etc/anywherelan"
