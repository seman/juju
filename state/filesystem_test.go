// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state_test

import (
	"github.com/juju/errors"
	"github.com/juju/names"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/instance"
	"github.com/juju/juju/state"
	"github.com/juju/juju/state/testing"
	"github.com/juju/juju/storage/provider/dummy"
	"github.com/juju/juju/storage/provider/registry"
)

type FilesystemStateSuite struct {
	StorageStateSuiteBase
}

var _ = gc.Suite(&FilesystemStateSuite{})

func (s *FilesystemStateSuite) TestAddServiceInvalidPool(c *gc.C) {
	ch := s.AddTestingCharm(c, "storage-filesystem")
	storage := map[string]state.StorageConstraints{
		"data": makeStorageCons("invalid-pool", 1024, 1),
	}
	_, err := s.State.AddService("storage-filesystem", s.Owner.String(), ch, nil, storage)
	c.Assert(err, gc.ErrorMatches, `.* pool "invalid-pool" not found`)
}

func (s *FilesystemStateSuite) TestAddServiceNoPool(c *gc.C) {
	ch := s.AddTestingCharm(c, "storage-filesystem")
	storage := map[string]state.StorageConstraints{
		"data": makeStorageCons("", 1024, 1),
	}
	svc, err := s.State.AddService("storage-filesystem", s.Owner.String(), ch, nil, storage)
	c.Assert(err, jc.ErrorIsNil)
	cons, err := svc.StorageConstraints()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(cons, jc.DeepEquals, map[string]state.StorageConstraints{
		"data": state.StorageConstraints{
			Pool:  "rootfs",
			Size:  1024,
			Count: 1,
		},
	})
}

func (s *FilesystemStateSuite) TestAddFilesystemWithoutBackingVolume(c *gc.C) {
	s.addUnitWithFilesystem(c, "rootfs", false)
}

func (s *FilesystemStateSuite) TestAddFilesystemWithBackingVolume(c *gc.C) {
	s.addUnitWithFilesystem(c, "loop", true)
}

func (s *FilesystemStateSuite) TestSetFilesystemInfoImmutable(c *gc.C) {
	_, u, storageTag := s.setupSingleStorage(c, "filesystem", "rootfs")
	err := s.State.AssignUnit(u, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)
	filesystem := s.storageInstanceFilesystem(c, storageTag)
	filesystemTag := filesystem.FilesystemTag()

	assignedMachineId, err := u.AssignedMachineId()
	c.Assert(err, jc.ErrorIsNil)
	machine, err := s.State.Machine(assignedMachineId)
	c.Assert(err, jc.ErrorIsNil)
	err = machine.SetProvisioned("inst-id", "fake_nonce", nil)
	c.Assert(err, jc.ErrorIsNil)

	filesystemInfoSet := state.FilesystemInfo{Size: 123}
	err = s.State.SetFilesystemInfo(filesystem.FilesystemTag(), filesystemInfoSet)
	c.Assert(err, jc.ErrorIsNil)

	// The first call to SetFilesystemInfo takes the pool name from
	// the params; the second does not, but it must not change
	// either. Callers are expected to get the existing info and
	// update it, leaving immutable values intact.
	err = s.State.SetFilesystemInfo(filesystem.FilesystemTag(), filesystemInfoSet)
	c.Assert(err, gc.ErrorMatches, `cannot set info for filesystem "0/0": cannot change pool from "rootfs" to ""`)

	filesystemInfoSet.Pool = "rootfs"
	s.assertFilesystemInfo(c, filesystemTag, filesystemInfoSet)
}

func (s *FilesystemStateSuite) TestVolumeFilesystem(c *gc.C) {
	filesystemAttachment, _ := s.addUnitWithFilesystem(c, "loop", true)
	filesystem := s.filesystem(c, filesystemAttachment.Filesystem())
	_, err := filesystem.Info()
	c.Assert(err, jc.Satisfies, errors.IsNotProvisioned)

	volumeTag, err := filesystem.Volume()
	c.Assert(err, jc.ErrorIsNil)
	filesystem = s.volumeFilesystem(c, volumeTag)
	c.Assert(filesystem.FilesystemTag(), gc.Equals, filesystemAttachment.Filesystem())
}

func (s *FilesystemStateSuite) addUnitWithFilesystem(c *gc.C, pool string, withVolume bool) (state.FilesystemAttachment, state.StorageAttachment) {
	ch := s.AddTestingCharm(c, "storage-filesystem")
	storage := map[string]state.StorageConstraints{
		"data": makeStorageCons(pool, 1024, 1),
	}
	service := s.AddTestingServiceWithStorage(c, "storage-filesystem", ch, storage)
	unit, err := service.AddUnit()
	c.Assert(err, jc.ErrorIsNil)
	err = s.State.AssignUnit(unit, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)
	assignedMachineId, err := unit.AssignedMachineId()
	c.Assert(err, jc.ErrorIsNil)
	assignedMachineTag := names.NewMachineTag(assignedMachineId)

	storageAttachments, err := s.State.UnitStorageAttachments(unit.UnitTag())
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(storageAttachments, gc.HasLen, 1)
	storageInstance, err := s.State.StorageInstance(storageAttachments[0].StorageInstance())
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(storageInstance.Kind(), gc.Equals, state.StorageKindFilesystem)

	filesystem := s.storageInstanceFilesystem(c, storageInstance.StorageTag())
	c.Assert(filesystem.FilesystemTag(), gc.Equals, names.NewFilesystemTag("0/0"))
	filesystemStorageTag, err := filesystem.Storage()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(filesystemStorageTag, gc.Equals, storageInstance.StorageTag())
	_, err = filesystem.Info()
	c.Assert(err, jc.Satisfies, errors.IsNotProvisioned)
	_, ok := filesystem.Params()
	c.Assert(ok, jc.IsTrue)

	volume, err := s.State.StorageInstanceVolume(storageInstance.StorageTag())
	if withVolume {
		c.Assert(err, jc.ErrorIsNil)
		c.Assert(volume.VolumeTag(), gc.Equals, names.NewVolumeTag("0/0"))
		volumeStorageTag, err := volume.StorageInstance()
		c.Assert(err, jc.ErrorIsNil)
		c.Assert(volumeStorageTag, gc.Equals, storageInstance.StorageTag())
		filesystemVolume, err := filesystem.Volume()
		c.Assert(err, jc.ErrorIsNil)
		c.Assert(filesystemVolume, gc.Equals, volume.VolumeTag())
		_, err = s.State.VolumeAttachment(assignedMachineTag, filesystemVolume)
		c.Assert(err, jc.ErrorIsNil)
	} else {
		c.Assert(err, jc.Satisfies, errors.IsNotFound)
		_, err = filesystem.Volume()
		c.Assert(errors.Cause(err), gc.Equals, state.ErrNoBackingVolume)
	}

	machine, err := s.State.Machine(assignedMachineId)
	c.Assert(err, jc.ErrorIsNil)
	filesystemAttachments, err := s.State.MachineFilesystemAttachments(assignedMachineTag)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(filesystemAttachments, gc.HasLen, 1)
	c.Assert(filesystemAttachments[0].Filesystem(), gc.Equals, filesystem.FilesystemTag())
	c.Assert(filesystemAttachments[0].Machine(), gc.Equals, machine.MachineTag())
	_, err = filesystemAttachments[0].Info()
	c.Assert(err, jc.Satisfies, errors.IsNotProvisioned)
	_, ok = filesystemAttachments[0].Params()
	c.Assert(ok, jc.IsTrue)

	att, err := s.State.FilesystemAttachment(machine.MachineTag(), filesystem.FilesystemTag())
	c.Assert(err, jc.ErrorIsNil)
	return att, storageAttachments[0]
}

func (s *FilesystemStateSuite) TestWatchFilesystemAttachment(c *gc.C) {
	_, u, storageTag := s.setupSingleStorage(c, "filesystem", "rootfs")
	err := s.State.AssignUnit(u, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)
	assignedMachineId, err := u.AssignedMachineId()
	c.Assert(err, jc.ErrorIsNil)
	machineTag := names.NewMachineTag(assignedMachineId)

	filesystem := s.storageInstanceFilesystem(c, storageTag)
	filesystemTag := filesystem.FilesystemTag()

	w := s.State.WatchFilesystemAttachment(machineTag, filesystemTag)
	defer testing.AssertStop(c, w)
	wc := testing.NewNotifyWatcherC(c, s.State, w)
	wc.AssertOneChange()

	machine, err := s.State.Machine(assignedMachineId)
	c.Assert(err, jc.ErrorIsNil)
	err = machine.SetProvisioned("inst-id", "fake_nonce", nil)
	c.Assert(err, jc.ErrorIsNil)

	// filesystem attachment will NOT react to filesystem changes
	err = s.State.SetFilesystemInfo(filesystemTag, state.FilesystemInfo{
		FilesystemId: "fs-123",
	})
	c.Assert(err, jc.ErrorIsNil)
	wc.AssertNoChange()

	err = s.State.SetFilesystemAttachmentInfo(
		machineTag, filesystemTag, state.FilesystemAttachmentInfo{
			MountPoint: "/srv",
		},
	)
	c.Assert(err, jc.ErrorIsNil)
	wc.AssertOneChange()
}

func (s *FilesystemStateSuite) TestFilesystemInfo(c *gc.C) {
	_, u, storageTag := s.setupSingleStorage(c, "filesystem", "rootfs")
	err := s.State.AssignUnit(u, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)
	assignedMachineId, err := u.AssignedMachineId()
	c.Assert(err, jc.ErrorIsNil)
	machineTag := names.NewMachineTag(assignedMachineId)

	filesystem := s.storageInstanceFilesystem(c, storageTag)
	filesystemTag := filesystem.FilesystemTag()

	s.assertFilesystemUnprovisioned(c, filesystemTag)
	s.assertFilesystemAttachmentUnprovisioned(c, machineTag, filesystemTag)

	machine, err := s.State.Machine(assignedMachineId)
	c.Assert(err, jc.ErrorIsNil)
	err = machine.SetProvisioned("inst-id", "fake_nonce", nil)
	c.Assert(err, jc.ErrorIsNil)

	filesystemInfo := state.FilesystemInfo{FilesystemId: "fs-123", Size: 456}
	err = s.State.SetFilesystemInfo(filesystemTag, filesystemInfo)
	c.Assert(err, jc.ErrorIsNil)
	filesystemInfo.Pool = "rootfs" // taken from params
	s.assertFilesystemInfo(c, filesystemTag, filesystemInfo)
	s.assertFilesystemAttachmentUnprovisioned(c, machineTag, filesystemTag)

	filesystemAttachmentInfo := state.FilesystemAttachmentInfo{MountPoint: "/srv"}
	err = s.State.SetFilesystemAttachmentInfo(machineTag, filesystemTag, filesystemAttachmentInfo)
	c.Assert(err, jc.ErrorIsNil)
	s.assertFilesystemAttachmentInfo(c, machineTag, filesystemTag, filesystemAttachmentInfo)
}

func (s *FilesystemStateSuite) TestVolumeBackedFilesystemScope(c *gc.C) {
	_, unit, storageTag := s.setupSingleStorage(c, "filesystem", "environscoped-block")
	err := s.State.AssignUnit(unit, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)

	filesystem := s.storageInstanceFilesystem(c, storageTag)
	c.Assert(filesystem.Tag(), gc.Equals, names.NewFilesystemTag("0/0"))
	volumeTag, err := filesystem.Volume()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(volumeTag, gc.Equals, names.NewVolumeTag("0"))
}

func (s *FilesystemStateSuite) TestWatchEnvironFilesystems(c *gc.C) {
	service := s.setupMixedScopeStorageService(c, "filesystem")
	addUnit := func() {
		u, err := service.AddUnit()
		c.Assert(err, jc.ErrorIsNil)
		err = s.State.AssignUnit(u, state.AssignCleanEmpty)
		c.Assert(err, jc.ErrorIsNil)
	}
	addUnit()

	w := s.State.WatchEnvironFilesystems()
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChangeInSingleEvent("0") // initial
	wc.AssertNoChange()

	addUnit()
	wc.AssertChangeInSingleEvent("3")
	wc.AssertNoChange()

	// TODO(axw) respond to Dying/Dead when we have
	// the means to progress Filesystem lifecycle.
}

func (s *FilesystemStateSuite) TestWatchEnvironFilesystemAttachments(c *gc.C) {
	service := s.setupMixedScopeStorageService(c, "filesystem")
	addUnit := func() {
		u, err := service.AddUnit()
		c.Assert(err, jc.ErrorIsNil)
		err = s.State.AssignUnit(u, state.AssignCleanEmpty)
		c.Assert(err, jc.ErrorIsNil)
	}
	addUnit()

	w := s.State.WatchEnvironFilesystemAttachments()
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChangeInSingleEvent("0:0") // initial
	wc.AssertNoChange()

	addUnit()
	wc.AssertChangeInSingleEvent("1:3")
	wc.AssertNoChange()

	// TODO(axw) respond to Dying/Dead when we have
	// the means to progress Volume lifecycle.
}

func (s *FilesystemStateSuite) TestWatchMachineFilesystems(c *gc.C) {
	service := s.setupMixedScopeStorageService(c, "filesystem")
	addUnit := func() {
		u, err := service.AddUnit()
		c.Assert(err, jc.ErrorIsNil)
		err = s.State.AssignUnit(u, state.AssignCleanEmpty)
		c.Assert(err, jc.ErrorIsNil)
	}
	addUnit()

	w := s.State.WatchMachineFilesystems(names.NewMachineTag("0"))
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChangeInSingleEvent("0/1", "0/2") // initial
	wc.AssertNoChange()

	addUnit()
	// no change, since we're only interested in the one machine.
	wc.AssertNoChange()

	// TODO(axw) respond to Dying/Dead when we have
	// the means to progress Filesystem lifecycle.
}

func (s *FilesystemStateSuite) TestWatchMachineFilesystemAttachments(c *gc.C) {
	service := s.setupMixedScopeStorageService(c, "filesystem")
	addUnit := func() {
		u, err := service.AddUnit()
		c.Assert(err, jc.ErrorIsNil)
		err = s.State.AssignUnit(u, state.AssignCleanEmpty)
		c.Assert(err, jc.ErrorIsNil)
	}
	addUnit()

	w := s.State.WatchMachineFilesystemAttachments(names.NewMachineTag("0"))
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChangeInSingleEvent("0:0", "0:0/1", "0:0/2") // initial
	wc.AssertNoChange()

	addUnit()
	// no change, since we're only interested in the one machine.
	wc.AssertNoChange()

	// TODO(axw) respond to changes to the same machine when we support
	// dynamic storage and/or placement.
	// TODO(axw) respond to Dying/Dead when we have
	// the means to progress Filesystem lifecycle.
}

func (s *FilesystemStateSuite) TestParseFilesystemAttachmentId(c *gc.C) {
	assertValid := func(id string, m names.MachineTag, v names.FilesystemTag) {
		machineTag, filesystemTag, err := state.ParseFilesystemAttachmentId(id)
		c.Assert(err, jc.ErrorIsNil)
		c.Assert(machineTag, gc.Equals, m)
		c.Assert(filesystemTag, gc.Equals, v)
	}
	assertValid("0:0", names.NewMachineTag("0"), names.NewFilesystemTag("0"))
	assertValid("0:0/1", names.NewMachineTag("0"), names.NewFilesystemTag("0/1"))
	assertValid("0/lxc/0:1", names.NewMachineTag("0/lxc/0"), names.NewFilesystemTag("1"))
}

func (s *FilesystemStateSuite) TestParseFilesystemAttachmentIdError(c *gc.C) {
	assertError := func(id, expect string) {
		_, _, err := state.ParseFilesystemAttachmentId(id)
		c.Assert(err, gc.ErrorMatches, expect)
	}
	assertError("", `invalid filesystem attachment ID ""`)
	assertError("0", `invalid filesystem attachment ID "0"`)
	assertError("0:foo", `invalid filesystem attachment ID "0:foo"`)
	assertError("bar:0", `invalid filesystem attachment ID "bar:0"`)
}

func (s *FilesystemStateSuite) TestAssignToMachine(c *gc.C) {
	_, unit, _ := s.setupSingleStorage(c, "filesystem", "loop-pool")
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, jc.ErrorIsNil)
	err = unit.AssignToMachine(machine)
	c.Assert(err, jc.ErrorIsNil)
	filesystemAttachments, err := s.State.MachineFilesystemAttachments(machine.MachineTag())
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(filesystemAttachments, gc.HasLen, 1)
}

func (s *FilesystemStateSuite) TestAssignToMachineErrors(c *gc.C) {
	registry.RegisterProvider("static", &dummy.StorageProvider{
		IsDynamic: false,
	})
	registry.RegisterEnvironStorageProviders("someprovider", "static")
	defer registry.RegisterProvider("static", nil)

	_, unit, _ := s.setupSingleStorage(c, "filesystem", "static")
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, jc.ErrorIsNil)
	err = unit.AssignToMachine(machine)
	c.Assert(err, gc.ErrorMatches, `cannot assign unit "storage-filesystem/0" to machine 0: static storage provider does not support dynamic storage`)

	container, err := s.State.AddMachineInsideMachine(state.MachineTemplate{
		Series: "quantal",
		Jobs:   []state.MachineJob{state.JobHostUnits},
	}, machine.Id(), instance.LXC)
	c.Assert(err, jc.ErrorIsNil)
	err = unit.AssignToMachine(container)
	c.Assert(err, gc.ErrorMatches, `cannot assign unit "storage-filesystem/0" to machine 0/lxc/0: adding storage to lxc container not supported`)
}

func (s *FilesystemStateSuite) TestRemoveStorageInstanceUnassignsFilesystem(c *gc.C) {
	filesystemAttachment, storageAttachment := s.addUnitWithFilesystem(c, "loop", true)
	filesystem := s.filesystem(c, filesystemAttachment.Filesystem())
	volume := s.filesystemVolume(c, filesystemAttachment.Filesystem())
	storageTag := storageAttachment.StorageInstance()
	unitTag := storageAttachment.Unit()

	err := s.State.DestroyStorageInstance(storageTag)
	c.Assert(err, jc.ErrorIsNil)
	err = s.State.DestroyStorageAttachment(storageTag, unitTag)
	c.Assert(err, jc.ErrorIsNil)

	// The storage instance and attachment are dying, but not yet
	// removed from state. The filesystem should still be assigned.
	s.storageInstanceFilesystem(c, storageTag)
	s.storageInstanceVolume(c, storageTag)

	err = s.State.RemoveStorageAttachment(storageTag, unitTag)
	c.Assert(err, jc.ErrorIsNil)

	// The storage instance is now gone; the filesystem should no longer
	// be assigned to the storage.
	_, err = s.State.StorageInstanceFilesystem(storageTag)
	c.Assert(err, gc.ErrorMatches, `filesystem for storage instance "data/0" not found`)
	_, err = s.State.StorageInstanceVolume(storageTag)
	c.Assert(err, gc.ErrorMatches, `volume for storage instance "data/0" not found`)

	// The filesystem and volume should not have been destroyed, though.
	s.filesystem(c, filesystem.FilesystemTag())
	s.volume(c, volume.VolumeTag())
}

func (s *FilesystemStateSuite) TestSetFilesystemAttachmentInfoFilesystemNotProvisioned(c *gc.C) {
	filesystemAttachment, _ := s.addUnitWithFilesystem(c, "rootfs", false)
	err := s.State.SetFilesystemAttachmentInfo(
		filesystemAttachment.Machine(),
		filesystemAttachment.Filesystem(),
		state.FilesystemAttachmentInfo{},
	)
	c.Assert(err, gc.ErrorMatches, `cannot set info for filesystem attachment 0/0:0: filesystem "0/0" not provisioned`)
}

func (s *FilesystemStateSuite) TestSetFilesystemAttachmentInfoMachineNotProvisioned(c *gc.C) {
	filesystemAttachment, _ := s.addUnitWithFilesystem(c, "rootfs", false)
	err := s.State.SetFilesystemInfo(
		filesystemAttachment.Filesystem(),
		state.FilesystemInfo{Size: 123},
	)
	c.Assert(err, jc.ErrorIsNil)
	err = s.State.SetFilesystemAttachmentInfo(
		filesystemAttachment.Machine(),
		filesystemAttachment.Filesystem(),
		state.FilesystemAttachmentInfo{},
	)
	c.Assert(err, gc.ErrorMatches, `cannot set info for filesystem attachment 0/0:0: machine 0 not provisioned`)
}

func (s *FilesystemStateSuite) TestSetFilesystemInfoVolumeAttachmentNotProvisioned(c *gc.C) {
	filesystemAttachment, _ := s.addUnitWithFilesystem(c, "loop", true)
	err := s.State.SetFilesystemInfo(
		filesystemAttachment.Filesystem(),
		state.FilesystemInfo{Size: 123},
	)
	c.Assert(err, gc.ErrorMatches, `cannot set info for filesystem "0/0": volume attachment "0/0" on "0" not provisioned`)
}
