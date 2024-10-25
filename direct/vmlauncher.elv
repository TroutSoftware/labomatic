#! /usr/bin/elvish
#
# VM runner script
#
# QEMU is a beast of a command-line, so this script wraps the common options
# tu run builder-style virtual machines

use flag

fn run_qemu { | hda nic &install=""|
	var address = (cat /sys/class/net/$nic/address)
	var tapname = /dev/tap(cat /sys/class/net/$nic/ifindex)

	var args = [
			-machine accel=kvm,type=q35
			-cpu host
			-smp 4
			-m 8G

			# TODO only run when needed
			-nographic -vnc 172.31.112.12:0
			
			-object rng-random,id=rng0,filename=/dev/urandom
			-device virtio-rng-pci,rng=rng0

			# hard-drive
			-device virtio-scsi-pci,id=scsi0
			-drive file=$hda,if=none,format=raw,discard=unmap,aio=native,cache=none,id=sda
			-device scsi-hd,drive=sda,bus=scsi0.0

			# network
			-nic tap,model=virtio,mac=$address,fd=3
	]

	if (!=s $install "") {
		set @args = $@args -cdrom $install -boot d
	}

	/usr/bin/qemu-system-x86_64 $@args 3<>$tapname
}

func start_tap { | base |
	var suffix = (dd if=/dev/urandom bs=5 count=1 | basenc --base32)
	var name = mac$suffix
	
	ip link add link $base name $name type macvtap
	ip link set $name up
	put $name
}

func stop_tap { | name |
	ip link set $name down
	ip link delete $name
}


var opts _ = (flag:parse $args [
  [nic eno1 'name of the network interface to use as base transport']
  [hda "" 'block device']
  [iso '' 'iso to boot from']
])

var tap = (start_tap $opts[nic])
run_qemu $opts[hda] $tap &install=$opts[iso]
stop_tap $tap