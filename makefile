# Package build
# =============
#
# The multiple elements of the package are provided

LABOMATIC_VERSION := 0.5.3

build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/labd build/labctl build/routeros.img
build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/nfpm nfpm.yaml
build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/initfs build/vmlinuz-cyos
build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: $(wildcard install/*)
	build/nfpm --packager deb --target build package

.PHONY: clean
clean:
	rm -r build/*
	rm deps.mkd

# Build dependencies
# ==================
# 
# Packages are build using [nfpm].
# The default router is built on a [mikrotik] image.
#
# Build dependencies will be checked
#
# [nfpm] https://nfpm.goreleaser.com/
# [mikrotik] https://mikrotik.com/download

ifeq (,$(wildcard build/vmlinuz-cyos))
	$(error "CyberOS kernel must be acquired manually")
endif

build/check: build/routeros.img build/nfpm
	sha256sum --check cksum | tee $@

.PHONY: postconfig
postconfig: build/routeros.img build/check
	echo "Starting RouterOS: configure a default admin password for later use"
	/usr/bin/qemu-system-x86_64 -machine accel=kvm,type=q35 -cpu host -nographic -drive format=qcow2,file=$<

NFPM_VERSION := 2.41.1
build/nfpm:
	curl -L -o build/nfpm_$(NFPM_VERSION).tgz https://github.com/goreleaser/nfpm/releases/download/v$(NFPM_VERSION)/nfpm_$(NFPM_VERSION)_Linux_x86_64.tar.gz
	tar -xf build/nfpm_$(NFPM_VERSION).tgz -C build

MTK_VERSION := 7.16.2
build/routeros.img:
	curl -L -o build/routeros.img.zip https://download.mikrotik.com/routeros/$(MTK_VERSION)/chr-$(MTK_VERSION).img.zip
	unzip -d build build/routeros.img.zip
	qemu-img convert -f raw -O qcow2 build/chr-$(MTK_VERSION).img $@

deps.mkd: $(wildcard *.go) $(wildcard cmd/labd/*.go) $(wildcard cmd/labctl/*.go) $(wildcard assets/uinit/*.go)
	godeps -o $@ ./cmd/labd ./cmd/labctl ./assets/uinit

include deps.mkd

build/uinit: github.com/TroutSoftware/labomatic/assets/uinit
	go build -o $@ $<

build/kragent: go.mod
	go build -tags osusergo,netgo -o $@ github.com/bradfitz/qemu-guest-kragent

build/initfs: build/uinit build/kragent assets/initfs.elv
	elvish assets/initfs.elv -o $@

build/labd: deps.mkd
build/labd: github.com/TroutSoftware/labomatic/cmd/labd
	go build -o $@ $<

build/labctl: deps.mkd
build/labctl: github.com/TroutSoftware/labomatic/cmd/labctl
	go build -o $@ $<
