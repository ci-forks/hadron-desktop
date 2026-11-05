# Build either Hadron desktop variant, the opt-in agent overlay, and bootable
# installer ISOs.
#
#   make image        # build the desktop image (Kairos init layer folded in)
#   make iso          # build the image + the installer ISO
#   make              # both (image, then iso)
#   make DESKTOP=i3 image               # XLibre + i3 variant
#   make DESKTOP=cosmic image           # COSMIC (stacking, for newcomers)
#   make DESKTOP=niri image             # niri + Noctalia (scrolling tiling)
#   make images                          # build both variant images
#   make agent-image                     # build the i3-based agent overlay
#   make agent-iso                       # build the agent ISO
#   make clean                           # remove build artifacts
#
# Knobs (override on the command line):
#   make DESKTOP=i3 GPU=full FIRMWARE=true # XLibre/i3 + hardware support
#   make IMAGE=sway-desktop:hw             # change the image tag
#   make BASE_IMAGE=ghcr.io/...:vX        # pin a different Hadron base
#   make VERSION=v1.2.3                   # stamp a Kairos version
#   make CACHE_REF=ghcr.io/o/r:cache      # reuse a registry build cache (CI)

DESKTOP      ?= sway
IMAGE        ?= $(DESKTOP)-desktop:dev
# Hadron release to build on. scripts/bump-hadron.sh rewrites this line together
# with the HADRON_VERSION defaults in the Dockerfiles.
HADRON_VERSION ?= v0.6.3
BASE_IMAGE   ?= ghcr.io/kairos-io/hadron:$(HADRON_VERSION)
AURORA_IMAGE ?= quay.io/kairos/auroraboot:v0.29.0
AGENT_BASE_IMAGE ?= i3-desktop:dev
AGENT_IMAGE      ?= agent-desktop:dev
AGENT_WORK       := build/agent-desktop
AGENT_ISO_DIR    := $(AGENT_WORK)/iso

VALID_DESKTOPS := sway i3 cosmic niri
ifneq ($(words $(DESKTOP)),1)
$(error unsupported DESKTOP '$(DESKTOP)'; choose exactly one of: $(VALID_DESKTOPS))
endif
ifeq ($(filter $(DESKTOP),$(VALID_DESKTOPS)),)
$(error unsupported DESKTOP '$(DESKTOP)'; choose one of: $(VALID_DESKTOPS))
endif

WORK    := build/$(DESKTOP)-desktop
ISO_DIR := $(WORK)/iso

# Optional build args, only passed when set (otherwise the Dockerfile defaults
# apply: GPU=vm, FIRMWARE=false, VERSION=v0.0.0).
BUILD_ARGS := --build-arg HADRON_VERSION=$(HADRON_VERSION) --build-arg BASE_IMAGE=$(BASE_IMAGE) --build-arg DESKTOP=$(DESKTOP)
ifdef GPU
BUILD_ARGS += --build-arg GPU=$(GPU)
endif
ifdef FIRMWARE
BUILD_ARGS += --build-arg FIRMWARE=$(FIRMWARE)
endif
ifdef VERSION
BUILD_ARGS += --build-arg VERSION=$(VERSION)
endif

# Registry build cache, for CI. A fresh runner rebuilds every stage from source
# (hours for LLVM, Mesa, COSMIC), so CI points CACHE_REF at a registry ref and
# each image reads <CACHE_REF>-<name>; CACHE_PUSH=true also writes it back.
# mode=max exports every stage, not only the final image's layers, which is
# what lets a later build skip the long stages. The push is best effort: a
# cache that fails to upload must not fail a build that already succeeded.
# Exporting needs the containerd image store when using the default builder.
CACHE_REF  ?=
CACHE_PUSH ?=
comma := ,
cache_from = --cache-from type=registry$(comma)ref=$(CACHE_REF)-$(1)
cache_to   = --cache-to type=registry$(comma)ref=$(CACHE_REF)-$(1)$(comma)mode=max$(comma)ignore-error=true
cache_args = $(if $(CACHE_REF),$(call cache_from,$(1)) $(if $(filter true,$(CACHE_PUSH)),$(call cache_to,$(1))))

export DOCKER_BUILDKIT := 1

.PHONY: all image images iso agent-image agent-iso vm vm-install check-boot-ux print-base-image clean

all: iso

# The desktop image, with the Kairos init layer folded in as the final stage
# (build `--target default` for the bare desktop image without it).
# --no-cache-filter on kairos: that stage runs kairos-init's `dracut -f`, which
# bakes the splash branding and everything else into /boot/initrd, and it
# follows the moving kairos-init:master tag. BuildKit happily caches the step
# anyway, silently shipping a stale initramfs. Busting it keeps the initramfs
# honest; it is the cheap stage at the very end.
image:
	docker build $(BUILD_ARGS) $(call cache_args,$(DESKTOP)) --no-cache-filter kairos -t $(IMAGE) .

images:
	$(MAKE) DESKTOP=sway image
	$(MAKE) DESKTOP=i3 image

agent-image:
	$(MAKE) DESKTOP=i3 IMAGE=$(AGENT_BASE_IMAGE) $(if $(VERSION),VERSION=$(VERSION),) image
	docker build -f Dockerfile.agent --target agent $(call cache_args,agent) \
	  --build-arg BASE_IMAGE=$(AGENT_BASE_IMAGE) \
	  --build-arg HADRON_VERSION=$(HADRON_VERSION) \
	  $(if $(VERSION),--build-arg VERSION=$(VERSION),) \
	  -t $(AGENT_IMAGE) .

agent-iso: agent-image
	AURORA_IMAGE=$(AURORA_IMAGE) auroraboot/build.sh $(AGENT_IMAGE) $(AGENT_ISO_DIR)

# Build the installer ISO with AuroraBoot via auroraboot/build.sh, which stages
# an --overlay-iso dir carrying our own live GRUB menu (branded, themed, quiet
# cmdline) so AuroraBoot keeps ours instead of writing its default Kairos menu.
# build.sh also creates the output dir and clears stale ISOs, which these
# targets used to do inline.
iso: image
	AURORA_IMAGE=$(AURORA_IMAGE) auroraboot/build.sh $(IMAGE) $(ISO_DIR)

# Run the image in QEMU with the correct flags (UEFI + virtio-gpu, NOT the
# default VGA which renders the boot console as garbled static). See tools/vm.sh
# for env knobs (NOVNC=1, FRESH=1, MEM, VNC, ...).
vm-install:           ## fresh disk, boot the newest installer ISO
	tools/vm.sh install
vm:                   ## boot the already-installed disk
	tools/vm.sh run

# Boot-UX assertions (splash, initramfs, branding, ISO overlay) for every variant
# image that is already built locally. Deliberately NOT a dependency of `all`:
# the checks inspect a built image, so wiring them into a plain `make` would
# either fail before the image exists or force a rebuild. Run them after
# building: `make image && make check-boot-ux`.
#
# Each variant is guarded on `docker image inspect` so having built only one
# variant checks only that one instead of erroring. The subtitle passed here is
# the assertion, not a lookup -- note the agent variant runs i3 and so is
# expected to say "i3 · xlibre · kairos", not "agent".
check-boot-ux:
	@ran=0; fail=0; \
	for spec in "sway-desktop:dev|sway · wayland · kairos" \
	            "i3-desktop:dev|i3 · xlibre · kairos" \
	            "cosmic-desktop:dev|cosmic · wayland · kairos" \
	            "niri-desktop:dev|niri · wayland · kairos" \
	            "agent-desktop:dev|i3 · xlibre · kairos"; do \
	  img="$${spec%%|*}"; subtitle="$${spec#*|}"; \
	  if docker image inspect "$$img" >/dev/null 2>&1; then \
	    echo "==> boot-ux: $$img ($$subtitle)"; \
	    ran=1; \
	    test/boot-ux/run.sh "$$img" "$$subtitle" || fail=1; \
	  else \
	    echo "==> boot-ux: skipping $$img (not built)"; \
	  fi; \
	done; \
	if [ "$$ran" -eq 0 ]; then \
	  echo "check-boot-ux: no variant image built; run 'make image' first" >&2; exit 1; \
	fi; \
	exit $$fail

# The resolved base reference, for CI steps that pull it before the build.
print-base-image:
	@echo $(BASE_IMAGE)

clean:
	rm -rf $(WORK) $(AGENT_WORK)
