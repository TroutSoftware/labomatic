module github.com/TroutSoftware/labomatic

go 1.23

require (
	github.com/godbus/dbus/v5 v5.1.0
	github.com/google/go-cmp v0.6.0
	github.com/insomniacslk/dhcp v0.0.0-20240829085014-a3a4c1f04475
	github.com/vishvananda/netlink v1.3.0
	github.com/vishvananda/netns v0.0.4
	go.starlark.net v0.0.0-20240725214946-42030a7cedce
	golang.org/x/crypto v0.27.0
	golang.org/x/sys v0.26.0
)

require (
	gopkg.in/yaml.v3 v3.0.1 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.70 // indirect
)

require (
	github.com/creack/pty/v2 v2.0.1
	github.com/josharian/native v1.1.0 // indirect
	github.com/landlock-lsm/go-landlock v0.0.0-20241014143150-479ddab4c04c
	github.com/mdlayher/packet v1.1.2 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/u-root/uio v0.0.0-20240224005618-d2acac8f3701 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
)

replace github.com/bradfitz/qemu-guest-kragent => github.com/romaindoumenc/qemu-guest-kragent v0.0.0-20241218162357-5041fb4c77e0
