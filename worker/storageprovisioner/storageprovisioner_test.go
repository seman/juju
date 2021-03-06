// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package storageprovisioner_test

import (
	"errors"
	"time"

	"github.com/juju/names"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/storage"
	"github.com/juju/juju/storage/provider/registry"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/worker/storageprovisioner"
)

type storageProvisionerSuite struct {
	coretesting.BaseSuite
	provider                *dummyProvider
	managedFilesystemSource *mockManagedFilesystemSource
}

var _ = gc.Suite(&storageProvisionerSuite{})

func (s *storageProvisionerSuite) SetUpTest(c *gc.C) {
	s.BaseSuite.SetUpTest(c)
	s.provider = &dummyProvider{dynamic: true}
	registry.RegisterProvider("dummy", s.provider)
	s.AddCleanup(func(*gc.C) {
		registry.RegisterProvider("dummy", nil)
	})

	s.managedFilesystemSource = nil
	s.PatchValue(
		storageprovisioner.NewManagedFilesystemSource,
		func(
			blockDevices map[names.VolumeTag]storage.BlockDevice,
			filesystems map[names.FilesystemTag]storage.Filesystem,
		) storage.FilesystemSource {
			s.managedFilesystemSource = &mockManagedFilesystemSource{
				blockDevices: blockDevices,
				filesystems:  filesystems,
			}
			return s.managedFilesystemSource
		},
	)
}

func (s *storageProvisionerSuite) TestStartStop(c *gc.C) {
	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"dir",
		newMockVolumeAccessor(),
		newMockFilesystemAccessor(),
		&mockLifecycleManager{},
		newMockEnvironAccessor(c),
		newMockMachineAccessor(c),
	)
	worker.Kill()
	c.Assert(worker.Wait(), gc.IsNil)
}

func (s *storageProvisionerSuite) TestVolumeAdded(c *gc.C) {
	expectedVolumes := []params.Volume{{
		VolumeTag: "volume-1",
		Info: params.VolumeInfo{
			VolumeId:   "id-1",
			HardwareId: "serial-1",
			Size:       1024,
			Persistent: true,
		},
	}, {
		VolumeTag: "volume-2",
		Info: params.VolumeInfo{
			VolumeId:   "id-2",
			HardwareId: "serial-2",
			Size:       1024,
		},
	}}
	expectedVolumeAttachments := []params.VolumeAttachment{{
		VolumeTag:  "volume-1",
		MachineTag: "machine-1",
		Info: params.VolumeAttachmentInfo{
			DeviceName: "/dev/sda1",
			ReadOnly:   true,
		},
	}, {
		VolumeTag:  "volume-2",
		MachineTag: "machine-1",
		Info: params.VolumeAttachmentInfo{
			DeviceName: "/dev/sda2",
		},
	}}

	volumeInfoSet := make(chan interface{})
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")
	volumeAccessor.setVolumeInfo = func(volumes []params.Volume) ([]params.ErrorResult, error) {
		defer close(volumeInfoSet)
		c.Assert(volumes, jc.SameContents, expectedVolumes)
		return nil, nil
	}

	volumeAttachmentInfoSet := make(chan interface{})
	volumeAccessor.setVolumeAttachmentInfo = func(volumeAttachments []params.VolumeAttachment) ([]params.ErrorResult, error) {
		defer close(volumeAttachmentInfoSet)
		c.Assert(volumeAttachments, jc.SameContents, expectedVolumeAttachments)
		return nil, nil
	}
	lifecycleManager := &mockLifecycleManager{}

	filesystemAccessor := newMockFilesystemAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	volumeAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{{
		MachineTag: "machine-1", AttachmentTag: "volume-1",
	}, {
		MachineTag: "machine-1", AttachmentTag: "volume-2",
	}}
	assertNoEvent(c, volumeAttachmentInfoSet, "volume attachment set")

	// The worker should create volumes according to ids "1" and "2".
	volumeAccessor.volumesWatcher.changes <- []string{"1", "2"}
	// ... but not until the environment config is available.
	assertNoEvent(c, volumeInfoSet, "volume info set")
	assertNoEvent(c, volumeAttachmentInfoSet, "volume attachment info set")
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, volumeInfoSet, "waiting for volume info to be set")
	waitChannel(c, volumeAttachmentInfoSet, "waiting for volume attachments to be set")
}

func (s *storageProvisionerSuite) TestFilesystemAdded(c *gc.C) {
	expectedFilesystems := []params.Filesystem{{
		FilesystemTag: "filesystem-1",
		Info: params.FilesystemInfo{
			FilesystemId: "id-1",
			Size:         1024,
		},
	}, {
		FilesystemTag: "filesystem-2",
		Info: params.FilesystemInfo{
			FilesystemId: "id-2",
			Size:         1024,
		},
	}}

	filesystemInfoSet := make(chan interface{})
	filesystemAccessor := newMockFilesystemAccessor()
	filesystemAccessor.setFilesystemInfo = func(filesystems []params.Filesystem) ([]params.ErrorResult, error) {
		defer close(filesystemInfoSet)
		c.Assert(filesystems, jc.SameContents, expectedFilesystems)
		return nil, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	volumeAccessor := newMockVolumeAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	// The worker should create filesystems according to ids "1" and "2".
	filesystemAccessor.filesystemsWatcher.changes <- []string{"1", "2"}
	// ... but not until the environment config is available.
	assertNoEvent(c, filesystemInfoSet, "filesystem info set")
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, filesystemInfoSet, "waiting for filesystem info to be set")
}

func (s *storageProvisionerSuite) TestVolumeNeedsInstance(c *gc.C) {
	volumeInfoSet := make(chan interface{})
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.setVolumeInfo = func([]params.Volume) ([]params.ErrorResult, error) {
		defer close(volumeInfoSet)
		return nil, nil
	}
	volumeAccessor.setVolumeAttachmentInfo = func([]params.VolumeAttachment) ([]params.ErrorResult, error) {
		return nil, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	filesystemAccessor := newMockFilesystemAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)
	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer worker.Wait()
	defer worker.Kill()

	volumeAccessor.volumesWatcher.changes <- []string{needsInstanceVolumeId}
	environAccessor.watcher.changes <- struct{}{}
	assertNoEvent(c, volumeInfoSet, "volume info set")
	machineAccessor.instanceIds[names.NewMachineTag("1")] = "inst-id"
	machineAccessor.watcher.changes <- struct{}{}
	waitChannel(c, volumeInfoSet, "waiting for volume info to be set")
}

func (s *storageProvisionerSuite) TestVolumeNonDynamic(c *gc.C) {
	volumeInfoSet := make(chan interface{})
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.setVolumeInfo = func([]params.Volume) ([]params.ErrorResult, error) {
		defer close(volumeInfoSet)
		return nil, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	filesystemAccessor := newMockFilesystemAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)
	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer worker.Wait()
	defer worker.Kill()

	// Volumes for non-dynamic providers should not be created.
	s.provider.dynamic = false
	environAccessor.watcher.changes <- struct{}{}
	volumeAccessor.volumesWatcher.changes <- []string{"1"}
	assertNoEvent(c, volumeInfoSet, "volume info set")
}

func (s *storageProvisionerSuite) TestVolumeAttachmentAdded(c *gc.C) {
	// We should get two volume attachments:
	//   - volume-1 to machine-1, because the volume and
	//     machine are provisioned, but the attachment is not.
	//   - volume-1 to machine-0, because the volume,
	//     machine, and attachment are provisioned, but
	//     in a previous session, so a reattachment is
	//     requested.
	expectedVolumeAttachments := []params.VolumeAttachment{{
		VolumeTag:  "volume-1",
		MachineTag: "machine-1",
		Info: params.VolumeAttachmentInfo{
			DeviceName: "/dev/sda1",
			ReadOnly:   true,
		},
	}, {
		VolumeTag:  "volume-1",
		MachineTag: "machine-0",
		Info: params.VolumeAttachmentInfo{
			DeviceName: "/dev/sda1",
			ReadOnly:   true,
		},
	}}

	var allVolumeAttachments []params.VolumeAttachment
	volumeAttachmentInfoSet := make(chan interface{})
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.setVolumeAttachmentInfo = func(volumeAttachments []params.VolumeAttachment) ([]params.ErrorResult, error) {
		allVolumeAttachments = append(allVolumeAttachments, volumeAttachments...)
		volumeAttachmentInfoSet <- nil
		return make([]params.ErrorResult, len(volumeAttachments)), nil
	}
	lifecycleManager := &mockLifecycleManager{}

	// volume-1, machine-0, and machine-1 are provisioned.
	volumeAccessor.provisionedVolumes["volume-1"] = params.Volume{
		VolumeTag: "volume-1",
		Info: params.VolumeInfo{
			VolumeId: "vol-123",
		},
	}
	volumeAccessor.provisionedMachines["machine-0"] = instance.Id("already-provisioned-0")
	volumeAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")

	// machine-0/volume-1 attachment is already created.
	// We should see a reattachment.
	alreadyAttached := params.MachineStorageId{
		MachineTag:    "machine-0",
		AttachmentTag: "volume-1",
	}
	volumeAccessor.provisionedAttachments[alreadyAttached] = params.VolumeAttachment{
		MachineTag: "machine-0",
		VolumeTag:  "volume-1",
	}

	filesystemAccessor := newMockFilesystemAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	volumeAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{{
		MachineTag: "machine-1", AttachmentTag: "volume-1",
	}, {
		MachineTag: "machine-1", AttachmentTag: "volume-2",
	}, {
		MachineTag: "machine-2", AttachmentTag: "volume-1",
	}, {
		MachineTag: "machine-0", AttachmentTag: "volume-1",
	}}
	assertNoEvent(c, volumeAttachmentInfoSet, "volume attachment info set")
	volumeAccessor.volumesWatcher.changes <- []string{"1"}
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, volumeAttachmentInfoSet, "waiting for volume attachments to be set")
	c.Assert(allVolumeAttachments, jc.SameContents, expectedVolumeAttachments)

	// Reattachment should only happen once per session.
	volumeAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{alreadyAttached}
	assertNoEvent(c, volumeAttachmentInfoSet, "volume attachment info set")
}

func (s *storageProvisionerSuite) TestFilesystemAttachmentAdded(c *gc.C) {
	// We should only get a single filesystem attachment, because it is the
	// only combination where both machine and filesystem are already
	// provisioned, and the attachmenti s not.
	// We should get two filesystem attachments:
	//   - filesystem-1 to machine-1, because the filesystem and
	//     machine are provisioned, but the attachment is not.
	//   - filesystem-1 to machine-0, because the filesystem,
	//     machine, and attachment are provisioned, but in a
	//     previous session, so a reattachment is requested.
	expectedFilesystemAttachments := []params.FilesystemAttachment{{
		FilesystemTag: "filesystem-1",
		MachineTag:    "machine-1",
		Info: params.FilesystemAttachmentInfo{
			MountPoint: "/srv/fs-123",
		},
	}, {
		FilesystemTag: "filesystem-1",
		MachineTag:    "machine-0",
		Info: params.FilesystemAttachmentInfo{
			MountPoint: "/srv/fs-123",
		},
	}}

	var allFilesystemAttachments []params.FilesystemAttachment
	filesystemAttachmentInfoSet := make(chan interface{})
	filesystemAccessor := newMockFilesystemAccessor()
	filesystemAccessor.setFilesystemAttachmentInfo = func(filesystemAttachments []params.FilesystemAttachment) ([]params.ErrorResult, error) {
		allFilesystemAttachments = append(allFilesystemAttachments, filesystemAttachments...)
		filesystemAttachmentInfoSet <- nil
		return make([]params.ErrorResult, len(filesystemAttachments)), nil
	}

	// filesystem-1 and machine-1 are provisioned.
	filesystemAccessor.provisionedFilesystems["filesystem-1"] = params.Filesystem{
		FilesystemTag: "filesystem-1",
		Info: params.FilesystemInfo{
			FilesystemId: "fs-123",
		},
	}
	filesystemAccessor.provisionedMachines["machine-0"] = instance.Id("already-provisioned-0")
	filesystemAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")

	// machine-0/filesystem-1 attachment is already created.
	// We should see a reattachment.
	alreadyAttached := params.MachineStorageId{
		MachineTag:    "machine-0",
		AttachmentTag: "filesystem-1",
	}
	filesystemAccessor.provisionedAttachments[alreadyAttached] = params.FilesystemAttachment{
		MachineTag:    "machine-0",
		FilesystemTag: "filesystem-1",
	}

	lifecycleManager := &mockLifecycleManager{}
	volumeAccessor := newMockVolumeAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	filesystemAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{{
		MachineTag: "machine-1", AttachmentTag: "filesystem-1",
	}, {
		MachineTag: "machine-1", AttachmentTag: "filesystem-2",
	}, {
		MachineTag: "machine-2", AttachmentTag: "filesystem-1",
	}, {
		MachineTag: "machine-0", AttachmentTag: "filesystem-1",
	}}
	// ... but not until the environment config is available.
	assertNoEvent(c, filesystemAttachmentInfoSet, "filesystem attachment info set")
	filesystemAccessor.filesystemsWatcher.changes <- []string{"1"}
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, filesystemAttachmentInfoSet, "waiting for filesystem attachments to be set")
	c.Assert(allFilesystemAttachments, jc.SameContents, expectedFilesystemAttachments)

	// Reattachment should only happen once per session.
	filesystemAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{alreadyAttached}
	assertNoEvent(c, filesystemAttachmentInfoSet, "filesystem attachment info set")
}

func (s *storageProvisionerSuite) TestCreateVolumeBackedFilesystem(c *gc.C) {
	filesystemInfoSet := make(chan interface{})
	filesystemAccessor := newMockFilesystemAccessor()
	filesystemAccessor.setFilesystemInfo = func(filesystems []params.Filesystem) ([]params.ErrorResult, error) {
		filesystemInfoSet <- filesystems
		return nil, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	volumeAccessor := newMockVolumeAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		names.NewMachineTag("0"),
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	volumeAccessor.blockDevices[params.MachineStorageId{
		MachineTag:    "machine-0",
		AttachmentTag: "volume-0-0",
	}] = storage.BlockDevice{
		DeviceName: "xvdf1",
		Size:       123,
	}
	filesystemAccessor.filesystemsWatcher.changes <- []string{"0/0", "0/1"}
	assertNoEvent(c, filesystemInfoSet, "filesystem info set")
	environAccessor.watcher.changes <- struct{}{}

	// Only the block device for volume 0/0 is attached at the moment,
	// so only the corresponding filesystem will be created.
	filesystemInfo := waitChannel(
		c, filesystemInfoSet,
		"waiting for filesystem info to be set",
	).([]params.Filesystem)
	c.Assert(filesystemInfo, jc.DeepEquals, []params.Filesystem{{
		FilesystemTag: "filesystem-0-0",
		Info: params.FilesystemInfo{
			FilesystemId: "xvdf1",
			Size:         123,
		},
	}})

	// If we now attach the block device for volume 0/1 and trigger the
	// notification, then the storage provisioner will wake up and create
	// the filesystem.
	volumeAccessor.blockDevices[params.MachineStorageId{
		MachineTag:    "machine-0",
		AttachmentTag: "volume-0-1",
	}] = storage.BlockDevice{
		DeviceName: "xvdf2",
		Size:       246,
	}
	volumeAccessor.blockDevicesWatcher.changes <- struct{}{}
	filesystemInfo = waitChannel(
		c, filesystemInfoSet,
		"waiting for filesystem info to be set",
	).([]params.Filesystem)
	c.Assert(filesystemInfo, jc.DeepEquals, []params.Filesystem{{
		FilesystemTag: "filesystem-0-1",
		Info: params.FilesystemInfo{
			FilesystemId: "xvdf2",
			Size:         246,
		},
	}})
}

func (s *storageProvisionerSuite) TestAttachVolumeBackedFilesystem(c *gc.C) {
	infoSet := make(chan interface{})
	filesystemAccessor := newMockFilesystemAccessor()
	filesystemAccessor.setFilesystemAttachmentInfo = func(attachments []params.FilesystemAttachment) ([]params.ErrorResult, error) {
		infoSet <- attachments
		return nil, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	volumeAccessor := newMockVolumeAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		names.NewMachineTag("0"),
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	filesystemAccessor.provisionedFilesystems["filesystem-0-0"] = params.Filesystem{
		FilesystemTag: "filesystem-0-0",
		VolumeTag:     "volume-0-0",
		Info: params.FilesystemInfo{
			FilesystemId: "whatever",
			Size:         123,
		},
	}
	filesystemAccessor.provisionedMachines["machine-0"] = instance.Id("already-provisioned-0")

	volumeAccessor.blockDevices[params.MachineStorageId{
		MachineTag:    "machine-0",
		AttachmentTag: "volume-0-0",
	}] = storage.BlockDevice{
		DeviceName: "xvdf1",
		Size:       123,
	}
	filesystemAccessor.attachmentsWatcher.changes <- []params.MachineStorageId{{
		MachineTag:    "machine-0",
		AttachmentTag: "filesystem-0-0",
	}}
	assertNoEvent(c, infoSet, "filesystem attachment info set")
	environAccessor.watcher.changes <- struct{}{}
	filesystemAccessor.filesystemsWatcher.changes <- []string{"0/0"}

	info := waitChannel(
		c, infoSet, "waiting for filesystem attachment info to be set",
	).([]params.FilesystemAttachment)
	c.Assert(info, jc.DeepEquals, []params.FilesystemAttachment{{
		FilesystemTag: "filesystem-0-0",
		MachineTag:    "machine-0",
		Info: params.FilesystemAttachmentInfo{
			MountPoint: "/mnt/xvdf1",
			ReadOnly:   true,
		},
	}})
}

func (s *storageProvisionerSuite) TestUpdateEnvironConfig(c *gc.C) {
	volumeAccessor := newMockVolumeAccessor()
	lifecycleManager := &mockLifecycleManager{}
	filesystemAccessor := newMockFilesystemAccessor()
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	volumeAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")
	s.provider.volumeSourceFunc = func(envConfig *config.Config, sourceConfig *storage.Config) (storage.VolumeSource, error) {
		c.Assert(envConfig, gc.NotNil)
		c.Assert(sourceConfig, gc.NotNil)
		c.Assert(envConfig.AllAttrs()["foo"], gc.Equals, "bar")
		return nil, errors.New("zinga")
	}

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer worker.Wait()
	defer worker.Kill()

	newConfig, err := environAccessor.cfg.Apply(map[string]interface{}{"foo": "bar"})
	c.Assert(err, jc.ErrorIsNil)

	environAccessor.watcher.changes <- struct{}{}
	environAccessor.setConfig(newConfig)
	environAccessor.watcher.changes <- struct{}{}
	volumeAccessor.volumesWatcher.changes <- []string{"1", "2"}

	err = worker.Wait()
	c.Assert(err, gc.ErrorMatches, `processing pending volumes: creating volumes: getting volume source: getting storage source "dummy": zinga`)
}

func (s *storageProvisionerSuite) TestResourceTags(c *gc.C) {
	volumeInfoSet := make(chan interface{})
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")
	volumeAccessor.setVolumeInfo = func(volumes []params.Volume) ([]params.ErrorResult, error) {
		defer close(volumeInfoSet)
		return nil, nil
	}

	filesystemInfoSet := make(chan interface{})
	filesystemAccessor := newMockFilesystemAccessor()
	filesystemAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")
	filesystemAccessor.setFilesystemInfo = func(filesystems []params.Filesystem) ([]params.ErrorResult, error) {
		defer close(filesystemInfoSet)
		return nil, nil
	}

	var volumeSource dummyVolumeSource
	s.provider.volumeSourceFunc = func(envConfig *config.Config, sourceConfig *storage.Config) (storage.VolumeSource, error) {
		return &volumeSource, nil
	}

	var filesystemSource dummyFilesystemSource
	s.provider.filesystemSourceFunc = func(envConfig *config.Config, sourceConfig *storage.Config) (storage.FilesystemSource, error) {
		return &filesystemSource, nil
	}

	lifecycleManager := &mockLifecycleManager{}
	environAccessor := newMockEnvironAccessor(c)
	machineAccessor := newMockMachineAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		filesystemAccessor,
		lifecycleManager,
		environAccessor,
		machineAccessor,
	)
	defer func() { c.Assert(worker.Wait(), gc.IsNil) }()
	defer worker.Kill()

	volumeAccessor.volumesWatcher.changes <- []string{"1"}
	filesystemAccessor.filesystemsWatcher.changes <- []string{"1"}
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, volumeInfoSet, "waiting for volume info to be set")
	waitChannel(c, filesystemInfoSet, "waiting for filesystem info to be set")
	c.Assert(volumeSource.createVolumesArgs, jc.DeepEquals, [][]storage.VolumeParams{{{
		Tag:          names.NewVolumeTag("1"),
		Size:         1024,
		Provider:     "dummy",
		Attributes:   map[string]interface{}{"persistent": true},
		ResourceTags: map[string]string{"very": "fancy"},
		Attachment: &storage.VolumeAttachmentParams{
			Volume: names.NewVolumeTag("1"),
			AttachmentParams: storage.AttachmentParams{
				Machine:    names.NewMachineTag("1"),
				Provider:   "dummy",
				InstanceId: "already-provisioned-1",
				ReadOnly:   true,
			},
		},
	}}})
	c.Assert(filesystemSource.createFilesystemsArgs, jc.DeepEquals, [][]storage.FilesystemParams{{{
		Tag:          names.NewFilesystemTag("1"),
		Size:         1024,
		Provider:     "dummy",
		ResourceTags: map[string]string{"very": "fancy"},
	}}})
}

func (s *storageProvisionerSuite) TestSetVolumeInfoErrorStopsWorker(c *gc.C) {
	volumeAccessor := newMockVolumeAccessor()
	volumeAccessor.provisionedMachines["machine-1"] = instance.Id("already-provisioned-1")
	volumeAccessor.setVolumeInfo = func(volumes []params.Volume) ([]params.ErrorResult, error) {
		return []params.ErrorResult{{Error: &params.Error{Message: "message", Code: "code"}}}, nil
	}

	environAccessor := newMockEnvironAccessor(c)

	worker := storageprovisioner.NewStorageProvisioner(
		coretesting.EnvironmentTag,
		"storage-dir",
		volumeAccessor,
		newMockFilesystemAccessor(),
		&mockLifecycleManager{},
		environAccessor,
		newMockMachineAccessor(c),
	)
	defer worker.Wait()
	defer worker.Kill()

	done := make(chan interface{})
	go func() {
		defer close(done)
		err := worker.Wait()
		c.Assert(err, gc.ErrorMatches, "processing pending volumes: publishing volume 1 to state: message")
	}()

	volumeAccessor.volumesWatcher.changes <- []string{"1"}
	environAccessor.watcher.changes <- struct{}{}
	waitChannel(c, done, "waiting for worker to exit")
}

func waitChannel(c *gc.C, ch <-chan interface{}, activity string) interface{} {
	select {
	case v := <-ch:
		return v
	case <-time.After(coretesting.LongWait):
		c.Fatalf("timed out " + activity)
		panic("unreachable")
	}
}

func assertNoEvent(c *gc.C, ch <-chan interface{}, event string) {
	select {
	case <-ch:
		c.Fatalf("unexpected " + event)
	case <-time.After(coretesting.ShortWait):
	}
}

// TODO(wallyworld) - test destroying volumes when done
