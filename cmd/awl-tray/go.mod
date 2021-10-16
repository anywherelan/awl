module awl-tray

go 1.16

require (
	github.com/Kodeworks/golang-image-ico v0.0.0-20141118225523-73f0f4cfade9
	github.com/anywherelan/awl v0.0.0-00010101000000-000000000000
	github.com/gen2brain/beeep v0.0.0-20210529141713-5586760f0cc1
	github.com/getlantern/systray v1.1.0
	github.com/godbus/dbus/v5 v5.0.5
	github.com/ipfs/go-log/v2 v2.3.0
	github.com/ncruces/zenity v0.7.7
	github.com/skratchdot/open-golang v0.0.0-20200116055534-eef842397966
)

replace (
	github.com/anywherelan/awl => ../../
	github.com/getlantern/systray => github.com/anywherelan/systray v0.0.0-20210712192351-1da45426321d
	github.com/godbus/dbus/v5 => github.com/pymq/dbus/v5 v5.0.5-0.20210710104724-7ba66a7d9a5a
	github.com/ipfs/go-log/v2 => github.com/anywherelan/go-log/v2 v2.0.3-0.20210308150645-ad120b957e42
)
