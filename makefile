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

build/check: build/routeros.img build/nfpm
	sha256sum --check cksum | tee $@

.PHONY: postconfig
postconfig: build/routeros.img build/check
	echo "Starting RouterOS: configure a default admin password for later use"
	/usr/bin/qemu-system-x86_64 -machine accel=kvm,type=q35 -cpu host -nographic -drive format=raw,file=$<

NFPM_VERSION := 2.41.1
build/nfpm:
	curl -L -o build/nfpm_$(NFPM_VERSION).tgz https://github.com/goreleaser/nfpm/releases/download/v$(NFPM_VERSION)/nfpm_$(NFPM_VERSION)_Linux_x86_64.tar.gz
	tar -xf build/nfpm_$(NFPM_VERSION).tgz -C build

MTK_VERSION := 7.16.2
build/routeros.img:
	# curl -L -o build/routeros.img.zip https://download.mikrotik.com/routeros/$(MTK_VERSION)/chr-$(MTK_VERSION).img.zip
	# unzip -d build build/routeros.img.zip
	mv build/chr-$(MTK_VERSION).img $@

deps.mkd:
	godeps -o $@ ./cmd/labd

include deps.mkd

build/labd: deps.mkd
build/labd: github.com/TroutSoftware/labomatic/cmd/labd
	go build -o $@ $<

LABOMATIC_VERSION := 0.5.1

build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/labd
build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/nfpm nfpm.yaml
build/labomatic_$(LABOMATIC_VERSION)_amd64.deb: build/labd
	build/nfpm --packager deb --target build package