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

// TODO do not mention tailscale?

Some use cases:

- connect to your home/work laptop with RDP/VNC/SSH, which is usually behind NAT. Much easier with awl instead of
  configuring port forwarding or using heavy VPNs
- get secure access to your selfhosted services like Nextcloud, Home Assistant or Bitwarden without exposing them to the
  internet
- as an alternative instead of ngrok to share your development server with someone on another device for demonstration
- you can use your old android device remotely with [scrcpy](https://github.com/Genymobile/scrcpy) + awl to run some
  android-only apps instead of using an emulator on your PC

// TODO: секция с "why anywherelan?" рассказывающая разницу с wireguard и прочими

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

## Architecture (TODO: or move down? or rename to "how it works" or similar)

## How it works

Awl mainly relies on two libraries: tun/[wintun](https://www.wintun.net/) driver for virtual network interface (
networking layer 3, IP) and [libp2p](https://libp2p.io/) as peer-to-peer networking stack.

As a transport awl uses QUIC or TCP with TLS on top. For Kademlia DHT

Как оно примерно работает:
Первым делом awl подключается к комьюнити бутстрап нодам (ссылка на сервер)

# Installation

For desktop there are two versions: `awl` and `awl-tray`. `awl` is mainly used for servers and other headless purposes
and `awl-tray` is for desktop usage: it has nice system tray service to quickly get status of the server,
start/stop/restart it or to see which peers are online. Both versions have web-based ui for configuration and
monitoring, and terminal interface [cli](#TODO).

## Linux

// TODO: неактуально
`sudo apt-get install -y zenity`

## Windows

## Android

Simply install apk from releases page and launch the application.

TODO: about request battery optimization

## Connecting peers # TODO rename as Setup ?

## Config

Sep 10 17:18:03 rombik awl[26818]: 2021-09-10T17:18:03.319+0300 WARN awl/config could not get user config directory:
neither $XDG_CONFIG_HOME nor $HOME are defined

TODO about config location

## Server cli / Terminal based client TODO

Both `awl` and `awl-tray` versions have

```
$ ./awl-tray cli -h                               
NAME:
   awl - p2p mesh vpn

USAGE:
   awl-tray cli [global options] command [command options] [arguments...]

VERSION:
   dev

COMMANDS:
   add_peer       Invite peer or accept existing invitation from this peer
   auth_requests  Print all incoming friend requests
   p2p_info       Print p2p debug info
   peers_status   Print peers status
   update         update awl to the latest version
   help, h        Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --api_addr value  awl api address, example: 127.0.0.1:8639
   --help, -h        show help (default: false)
   --version, -v     print the version (default: false)
```

## Upgrading

// TODO: киллер-фича, что ssh подключения не обрываются, даже если перезапустить awl на той стороне

# Roadmap

- add support for awl dns for android
- add support for macOS (should not be too much work, just need to find a mac for testing)
- performance improvements for vpn tunnel protocol
- exit nodes - let you route all internet traffic through other peers

# Contributing

