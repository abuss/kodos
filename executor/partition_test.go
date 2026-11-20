package main

// partition_test.go — the partition_disk guard (ops.go) has to understand the
// kernel's partition naming and what "this table already IS the config's
// layout" means. Bare-metal field round 2026-11-20: the guard counted existing
// partitions by matching device+"p", a form that only exists on nvme/mmcblk/loop
// names, so on a SATA /dev/sda it counted 0 of 2 planned and refused the install
// forever — and a count-only comparison would have let a foreign 4-partition
// table through and formatted the wrong-sized root.
import (
	"strings"
	"testing"
)

// sfdisk -d output shape for a GPT SATA disk, exactly as util-linux prints it
// (space before the colon on partition lines, name="..." for GPT labels).
const sdDump4 = `label: gpt
label-id: 0935E188-3741-4258-9A6B-386A4264F779
device: /dev/sda
unit: sectors
first-lba: 2048

/dev/sda1 : start=        2048, size=     1048576, type="C12A7328-F81F-11D2-BA4B-00A0C93EC93B", name="boot"
/dev/sda2 : start=     1050624, size=    80054688, type="0FC63DAF-8483-4772-8E79-3D69D8477DE4", name="root"
/dev/sda3 : start=    81106944, size=     2097152, type="0657FC6D-A4AB-43C4-84E5-0933C84B4F4F"
/dev/sda4 : start=    83204096, size=     1048576, type="0FC63DAF-8483-4772-8E79-3D69D8477DE4"
`

const nvmeDump2 = `label: gpt
device: /dev/nvme0n1
unit: sectors

/dev/nvme0n1p1 : start=        2048, size=     1048576, name="boot"
/dev/nvme0n1p2 : start=     1050624, size=   498079744, name="root"
`

func TestExistingPartitionsSataNaming(t *testing.T) {
	got := existingPartitions(sdDump4, "/dev/sda")
	if len(got) != 4 {
		t.Fatalf("sata /dev/sda1..4 must all count, got %v", got)
	}
	if got[0] != "/dev/sda1" {
		t.Fatalf("first partition = %q, want /dev/sda1", got[0])
	}
}

func TestExistingPartitionsNvmeNaming(t *testing.T) {
	got := existingPartitions(nvmeDump2, "/dev/nvme0n1")
	if len(got) != 2 {
		t.Fatalf("nvme nbd-style p names must count, got %v", got)
	}
}

// arch layout: 512MB esp "boot" + remainder "root"
func archSpecs() []partSpec {
	return []partSpec{
		{Index: 1, Name: "boot", Size: "512MB", Type: "esp"},
		{Index: 2, Name: "root", Size: "100%", Type: "linux"},
	}
}

func TestLayoutKeepsMatchingTable(t *testing.T) {
	keep, why := layoutDecision(nvmeDump2, "/dev/nvme0n1", archSpecs())
	if !keep {
		t.Fatalf("a table that IS the config layout must be kept, got refuse: %s", why)
	}
}

func TestLayoutRefusesForeignTable(t *testing.T) {
	keep, why := layoutDecision(sdDump4, "/dev/sda", archSpecs())
	if keep {
		t.Fatal("4 partitions vs 2 planned must not be treated as the config layout")
	}
	if !strings.Contains(why, "4") || !strings.Contains(why, "2") {
		t.Fatalf("refusal must name what it found and what the config wants: %s", why)
	}
}

func TestLayoutRefusesHalfDoneTable(t *testing.T) {
	half := `label: gpt
device: /dev/sda

/dev/sda1 : start= 2048, size= 1048576, name="boot"
`
	if keep, _ := layoutDecision(half, "/dev/sda", archSpecs()); keep {
		t.Fatal("one partition of two planned is a half-done table, not a match")
	}
}

// Right count, foreign names: reformatting these would destroy a dual-boot or
// previously-different layout that happens to have two partitions.
func TestLayoutRefusesRightCountForeignNames(t *testing.T) {
	foreign := `label: gpt
device: /dev/sda

/dev/sda1 : start= 2048, size= 1048576, name="EFI"
/dev/sda2 : start= 1050624, size= 99999999, name="Microsoft basic data"
`
	if keep, _ := layoutDecision(foreign, "/dev/sda", archSpecs()); keep {
		t.Fatal("2 foreign-named partitions must not satisfy the config layout")
	}
}

func TestParseSwapsListsActiveDevices(t *testing.T) {
	text := `Filename				Type		Size		Used	Priority
/dev/sda3                               partition	33554428	0	-2
/tmp/swapfile                           file		1048572	0	-3
`
	got := parseSwaps(text)
	if len(got) != 1 || got[0] != "/dev/sda3" {
		t.Fatalf("swap devices = %v, want [/dev/sda3]", got)
	}
}

func TestDiskInUseFlagsActiveSwap(t *testing.T) {
	mounts := parseMounts("/dev/sdb1 /media/usb ext4 rw 0 0\n")
	if !diskInUse("/dev/sda", mounts, []string{"/dev/sda3"}) {
		t.Fatal("active swap on a disk partition must make the disk in use")
	}
	if diskInUse("/dev/sda", mounts, nil) {
		t.Fatal("/dev/sda with nothing mounted or swapped is not in use")
	}
	if diskInUse("/dev/sdb", mounts, nil) == false {
		t.Fatal("mounted partition must make its whole disk in use")
	}
}
