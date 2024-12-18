use flag

var nargs _ = (flag:parse $args [
	[o "" "Archive to create"]
])

var layout = [
		-d /bin 
		-d /boot
		-d /dev
		-d /etc
		-d /media
		-d /proc
		-d /sys
		-d /sbin
		-d /tmp
		-l usr/bin,/bin
		-l usr/sbin,/bin
]

var cnfg = [
		-f etc/passwd,assets/etc/passwd,mode=0644
		-f etc/group,assets/etc/group,mode=0644
		-f etc/issue,assets/etc/issue,mode=0644
		-f etc/shadow,assets/etc/shadow,mode=0640
		-f etc/resolv.conf,assets/etc/resolv.conf,mode=0640
		-d etc/ssl,mode=0555
		-f etc/ssl/ca-bundle.pem,assets/etc/ca-bundle.pem,mode=0444
]

var ucpio = (go env GOPATH)/bin/ucpio

$ucpio ^
		$@layout ^
		$@cnfg ^
		-f init,build/uinit,mode=0750 ^
		-f bin/busybox,/usr/bin/busybox,mode=750 ^
		-f sbin/kragent,build/kragent,mode=750 ^
		| gzip --best > $nargs[o]
