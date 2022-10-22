<p align="center">
    <a href="https://github.com/anywherelan/awl/blob/master/LICENSE"><img alt="GitHub license" src="https://img.shields.io/github/license/anywherelan/awl?color=brightgreen"></a>
    <a href="https://github.com/anywherelan/awl/releases"><img alt="GitHub release" src="https://img.shields.io/github/v/release/anywherelan/awl" /></a>
    <a href="https://github.com/anywherelan/awl/actions/workflows/test.yml"><img alt="Test build status" src="https://github.com/anywherelan/awl/actions/workflows/test.yml/badge.svg" /></a>
</p>

> Disclaimer: Anywherelan is currently considered beta software. Be aware! Although I was using it for a long time with
> no issues in core functionality.

# About

Anywherelan (awl for brevity) is a mesh VPN project, similar to tinc, direct wireguard or tailscale. Awl makes it easy
to connect to any of your devices (at the IP protocol level) wherever they are.

Some use cases:

- connect to your home/work laptop with RDP/VNC/SSH, which is usually behind NAT. Much easier with awl instead of
  configuring port forwarding or using heavy VPNs
- get secure access to your selfhosted services like Nextcloud, Home Assistant or Bitwarden without exposing them to the
  internet
- as an alternative instead of ngrok to share your development server with someone on another device for demonstration
- you can use your old android device remotely with [scrcpy](https://github.com/Genymobile/scrcpy) + awl to run some
  android-only apps instead of using an emulator on your PC

## Features

- unlike many alternatives, it works fully peer-to-peer, no need to set up or trust any third-party coordination
  servers. Your traffic goes directly to other devices
- easy to use: just download the app, scan QR code of your device, and you're set up
- built-in support for NAT traversal
- if both devices don't have public IP addresses (thus peer-to-peer is unavailable), awl will send your encrypted data
  through community relays (donates for infrastructure are welcome!)
- TLS encryption
- DNS server built-in. It allows using domains for your devices, like `work-laptop.awl` instead of IP address
- works on Windows, Linux, Android

## :camera: Screenshots

TODO

## How it works

Awl mainly relies on two libraries: tun/[wintun](https://www.wintun.net/) driver for virtual network interface (
networking layer 3, IP) and [libp2p](https://libp2p.io/) as peer-to-peer networking stack.

As a transport awl uses QUIC or TCP with TLS on top. Awl
uses [DHT](https://en.wikipedia.org/wiki/Distributed_hash_table) for connecting between peers.

At first, awl connects to community [bootstrap nodes](https://github.com/anywherelan/awl-bootstrap-node), register
itself (send peer id and public ip addresses) and later asks for addresses of peers you want to connect (all known
peers). If peer does not have public addresses, peer could be reached out through bootstrap nodes.

# Installation

For desktop there are two versions: `awl` and `awl-tray`. `awl` is mainly used for servers and other headless purposes
and `awl-tray` is for desktop usage: it has nice system tray service to quickly get status of the vpn server,
start/stop/restart it or to see which peers are online. Both versions have web-based ui for configuration and
monitoring, and terminal interface [cli](#terminal-based-client).

First, download archive from [releases page](https://github.com/anywherelan/awl/releases), extract it to the place you
like

TODO: update after release

## Linux

Make sure `zenity` or `kdialog` are installed.

```
sudo apt install -y zenity
```

You need to run executable with sudo rights in order to get access to `/dev/tun` interface.

## Windows

TODO

You need to run program as administrator. This is needed because only admins can create virtual network interfaces.

It's known problem that some antivirus software may get false detection, in this case you need to manually allow this
application.

## Android

Simply install apk from [releases page](https://github.com/anywherelan/awl/releases) and launch the application.

## Connecting peers

TODO

## Config file location

Awl looks for config file `config_awl.json` in paths in this order:

- in directory provided by environment variable `AWL_DATA_DIR`, if set. If path does not exist or there is no config
  file, awl will initialize new config in this path
- in the same directory as executable (if config file exists here)
- in OS-specific config directory. For example, on Unix it's `$HOME/.config/anywherelan`, on Windows
  it's `%AppData%/anywherelan`. If there is no config here, awl will initialize new config in this path

Tip: you can force using config file in the same directory with executable by creating `config_awl.json` with
content `{}` before first launch.

It is not recommended to amend config file while application is still running.

## Terminal based client

Both `awl` and `awl-tray` versions have CLI to communicate with vpn server.

TODO: examples

```
$ ./awl-tray cli -h
NAME:
   awl - p2p mesh vpn

USAGE:
   awl-tray cli [global options] command [command options] [arguments...]

VERSION:
   v0.5.0

COMMANDS:
   me        Group of commands to work with your stats and settings
   peers     Group of commands to work with peers. Use to check friend requests and work with known peers
   log       Prints application logs (default print 10 logs from the end of logs)
   p2p_info  Prints p2p debug info
   update    Updates awl to the latest version
   help, h   Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --api_addr value  awl api address, example: 127.0.0.1:8639
   --help, -h        show help (default: false)
   --version, -v     print the version (default: false)
```

## Upgrading

TODO: update after release

On desktop (awl-tray) you can upgrade application by clicking `System tray icon` ➞ `Check for updates`.

To upgrade application from terminal, you need to stop awl and then run `./awl cli update`.

As alternative, you can download new version from [releases page](https://github.com/anywherelan/awl/releases) and
manually replace old files with new ones.

Note that you can easily restart `awl` on remote host while being connected to it by `awl` (through ssh for example) and
your connection won't be terminated.

# Roadmap

- add support for awl dns for android
- add support for macOS (should not be too much work, just need to find a mac for testing)
- performance improvements for vpn tunnel protocol
- exit nodes - let you route all internet traffic through other peers

# Contributing

TODO
