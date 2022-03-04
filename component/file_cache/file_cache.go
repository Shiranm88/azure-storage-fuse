/*
    _____           _____   _____   ____          ______  _____  ------
   |     |  |      |     | |     | |     |     | |       |            |
   |     |  |      |     | |     | |     |     | |       |            |
   | --- |  |      |     | |-----| |---- |     | |-----| |-----  ------
   |     |  |      |     | |     | |     |     |       | |       |
   | ____|  |_____ | ____| | ____| |     |_____|  _____| |_____  |_____


   Licensed under the MIT License <http://opensource.org/licenses/MIT>.

   Copyright © 2020-2022 Microsoft Corporation. All rights reserved.
   Author : <blobfusedev@microsoft.com>

   Permission is hereby granted, free of charge, to any person obtaining a copy
   of this software and associated documentation files (the "Software"), to deal
   in the Software without restriction, including without limitation the rights
   to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
   copies of the Software, and to permit persons to whom the Software is
   furnished to do so, subject to the following conditions:

   The above copyright notice and this permission notice shall be included in all
   copies or substantial portions of the Software.

   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
   IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
   FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
   AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
   LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
   OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
   SOFTWARE
*/

package file_cache

import (
	"blobfuse2/common"
	"blobfuse2/common/config"
	"blobfuse2/common/log"
	"blobfuse2/internal"
	"blobfuse2/internal/handlemap"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// Common structure for Component
type FileCache struct {
	internal.BaseComponent

	tmpPath   string
	fileLocks *common.LockMap
	policy    cachePolicy

	createEmptyFile bool
	allowNonEmpty   bool
	cacheTimeout    float64
	cleanupOnStart  bool
	policyTrace     bool
	missedChmodList sync.Map
	mountPath       string
	allowOther      bool
	directRead      bool

	defaultPermission os.FileMode
}

// Structure defining your config parameters
type FileCacheOptions struct {
	// e.g. var1 uint32 `config:"var1"`
	TmpPath string `config:"path" yaml:"path,omitempty"`
	Policy  string `config:"policy" yaml:"policy,omitempty"`

	Timeout     uint32 `config:"timeout-sec" yaml:"timeout-sec,omitempty"`
	MaxEviction uint32 `config:"max-eviction" yaml:"max-eviction,omitempty"`

	MaxSizeMB     float64 `config:"max-size-mb" yaml:"max-size-mb,omitempty"`
	HighThreshold uint32  `config:"high-threshold" yaml:"high-threshold,omitempty"`
	LowThreshold  uint32  `config:"low-threshold" yaml:"low-threshold,omitempty"`

	CreateEmptyFile bool `config:"create-empty-file" yaml:"create-empty-file,omitempty"`
	AllowNonEmpty   bool `config:"allow-non-empty-temp" yaml:"allow-non-empty-temp,omitempty"`
	CleanupOnStart  bool `config:"cleanup-on-start" yaml:"cleanup-on-start,omitempty"`

	EnablePolicyTrace bool `config:"policy-trace" yaml:"policy-trace,omitempty"`
	DirectRead        bool `config:"direct-read" yaml:"direct-read,omitempty"`
}

const (
	compName            = "file_cache"
	defaultMaxEviction  = 5000
	defaultMaxThreshold = 80
	defaultMinThreshold = 60
)

//  Verification to check satisfaction criteria with Component Interface
var _ internal.Component = &FileCache{}

func (c *FileCache) Name() string {
	return compName
}

func (c *FileCache) SetName(name string) {
	c.BaseComponent.SetName(name)
}

func (c *FileCache) SetNextComponent(nc internal.Component) {
	c.BaseComponent.SetNextComponent(nc)
}

func (c *FileCache) Priority() internal.ComponentPriority {
	return internal.EComponentPriority.LevelMid()
}

// Start : Pipeline calls this method to start the component functionality
//  this shall not block the call otherwise pipeline will not start
func (c *FileCache) Start(ctx context.Context) error {
	log.Trace("Starting component : %s", c.Name())

	if c.cleanupOnStart {
		c.TempCacheCleanup()
	}

	if c.policy == nil {
		return fmt.Errorf("FileCache::Start : No cache policy created")
	}

	c.policy.StartPolicy()
	return nil
}

// Stop : Stop the component functionality and kill all threads started
func (c *FileCache) Stop() error {
	log.Trace("Stopping component : %s", c.Name())

	c.policy.ShutdownPolicy()
	c.TempCacheCleanup()

	return nil
}

func (c *FileCache) TempCacheCleanup() error {
	// TODO : Cleanup temp cache dir before exit
	if !isLocalDirEmpty(c.tmpPath) {
		log.Err("FileCache::TempCacheCleanup : Cleaning up temp directory %s", c.tmpPath)

		dirents, err := os.ReadDir(c.tmpPath)
		if err != nil {
			return nil
		}

		for _, entry := range dirents {
			os.RemoveAll(filepath.Join(c.tmpPath, entry.Name()))
		}
	}

	return nil
}

// Configure : Pipeline will call this method after constructor so that you can read config and initialize yourself
//  Return failure if any config is not valid to exit the process
func (c *FileCache) Configure() error {
	log.Trace("FileCache::Configure : %s", c.Name())

	conf := FileCacheOptions{}
	err := config.UnmarshalKey(compName, &conf)
	if err != nil {
		log.Err("FileCache: config error [invalid config attributes]")
		return fmt.Errorf("config error in %s [%s]", c.Name(), err.Error())
	}

	c.createEmptyFile = conf.CreateEmptyFile
	c.cacheTimeout = float64(conf.Timeout)
	c.allowNonEmpty = conf.AllowNonEmpty
	c.cleanupOnStart = conf.CleanupOnStart
	c.policyTrace = conf.EnablePolicyTrace
	c.directRead = conf.DirectRead

	c.tmpPath = conf.TmpPath
	if c.tmpPath == "" {
		log.Err("FileCache: config error [tmp-path not set]")
		return fmt.Errorf("config error in %s error [tmp-path not set]", c.Name())
	}

	err = config.UnmarshalKey("mount-path", &c.mountPath)
	if err == nil && c.mountPath == c.tmpPath {
		log.Err("FileCache: config error [tmp-path is same as mount path]")
		return fmt.Errorf("config error in %s error [tmp-path is same as mount path]", c.Name())
	}

	// Extract values from 'conf' and store them as you wish here
	_, err = os.Stat(conf.TmpPath)
	if os.IsNotExist(err) {
		log.Err("FileCache: config error [tmp-path does not exist. attempting to create tmp-path.]")
		err := os.Mkdir(conf.TmpPath, os.FileMode(0755))
		if err != nil {
			log.Err("FileCache: config error creating directory after clean [%s]", err.Error())
			return fmt.Errorf("config error in %s [%s]", c.Name(), err.Error())
		}
	}

	if !isLocalDirEmpty(conf.TmpPath) && !c.allowNonEmpty {
		log.Err("FileCache: config error %s directory is not empty", conf.TmpPath)
		return fmt.Errorf("config error in %s [%s]", c.Name(), "temp directory not empty")
	}

	err = config.UnmarshalKey("allow-other", &c.allowOther)
	if err != nil {
		log.Err("FileCache::Configure : config error [unable to obtain allow-other]")
		return fmt.Errorf("config error in %s [%s]", c.Name(), err.Error())
	}

	if c.allowOther {
		c.defaultPermission = common.DefaultAllowOtherPermissionBits
	} else {
		c.defaultPermission = common.DefaultFilePermissionBits
	}

	cacheConfig := c.GetPolicyConfig(conf)

	switch strings.ToLower(conf.Policy) {
	case "lru":
		c.policy = NewLRUPolicy(cacheConfig)
	case "lfu":
		c.policy = NewLFUPolicy(cacheConfig)
	default:
		log.Info("FileCache::Configure : Using default eviction policy")
		c.policy = NewLRUPolicy(cacheConfig)
	}

	if c.policy == nil {
		log.Err("FileCache::Configure : failed to create cache eviction policy")
		return fmt.Errorf("config error in %s [%s]", c.Name(), "failed to create cache policy")
	}

	log.Info("FileCache::Configure : create-empty %t, cache-timeout %d, tmp-path %s",
		c.createEmptyFile, int(c.cacheTimeout), c.tmpPath)

	return nil
}

// OnConfigChange : If component has registered, on config file change this method is called
func (c *FileCache) OnConfigChange() {
	conf := FileCacheOptions{}
	err := config.UnmarshalKey(compName, &conf)
	if err != nil {
		log.Err("FileCache: config error [invalid config attributes]")
	}

	c.createEmptyFile = conf.CreateEmptyFile
	c.cacheTimeout = float64(conf.Timeout)
	c.policyTrace = conf.EnablePolicyTrace
	c.directRead = conf.DirectRead
	c.policy.UpdateConfig(c.GetPolicyConfig(conf))
}

func (c *FileCache) GetPolicyConfig(conf FileCacheOptions) cachePolicyConfig {
	if conf.MaxEviction == 0 {
		conf.MaxEviction = defaultMaxEviction
	}
	if conf.HighThreshold == 0 {
		conf.HighThreshold = defaultMaxThreshold
	}
	if conf.LowThreshold == 0 {
		conf.LowThreshold = defaultMinThreshold
	}

	cacheConfig := cachePolicyConfig{
		tmpPath:       conf.TmpPath,
		maxEviction:   conf.MaxEviction,
		highThreshold: float64(conf.HighThreshold),
		lowThreshold:  float64(conf.LowThreshold),
		cacheTimeout:  uint32(conf.Timeout),
		maxSizeMB:     conf.MaxSizeMB,
		fileLocks:     c.fileLocks,
		policyTrace:   conf.EnablePolicyTrace,
	}

	return cacheConfig
}

// isLocalDirEmpty: Whether or not the local directory is empty.
func isLocalDirEmpty(path string) bool {
	f, _ := os.Open(path)
	defer f.Close()

	_, err := f.Readdirnames(1)
	return err == io.EOF
}

// invalidateDirectory: Recursively invalidates a directory in the file cache.
func (fc *FileCache) invalidateDirectory(name string) error {
	log.Trace("FileCache::invalidateDirectory : %s", name)

	localPath := filepath.Join(fc.tmpPath, name)
	_, err := os.Stat(localPath)
	if os.IsNotExist(err) {
		log.Info("FileCache::invalidateDirectory : %s does not exist in local cache.", name)
		return nil
	} else if err != nil {
		log.Debug("FileCache::invalidateDirectory : %s stat err [%s].", name, err.Error())
		return err
	}
	// TODO : wouldn't this cause a race condition? a thread might get the lock before we purge - and the file would be non-existent
	filepath.WalkDir(localPath, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d != nil {
			log.Debug("FileCache::invalidateDirectory : %s (%d) getting removed from cache", path, d.IsDir())
			if !d.IsDir() {
				fc.policy.CachePurge(path)
			} else {
				os.Remove(path)
			}
		}
		return nil
	})

	os.Remove(localPath)
	return nil
}

// Note: The primary purpose of the file cache is to keep track of files that are opened by the user.
// So we do not need to support some APIs like Create Directory since the file cache will manage
// creating local directories as needed.

// DeleteDir: Recursively invalidate the directory and its children
func (fc *FileCache) DeleteDir(options internal.DeleteDirOptions) error {
	log.Trace("FileCache::DeleteDir : %s", options.Name)

	err := fc.NextComponent().DeleteDir(options)
	if err != nil {
		log.Err("FileCache::DeleteDir : %s failed", options.Name)
		// There is a chance that meta file for directory was not created in which case
		// rest api delete will fail while we still need to cleanup the local cache for the same
	}

	go fc.invalidateDirectory(options.Name)
	return err
}

// Creates a new object attribute
func newObjAttr(path string, info fs.FileInfo) *internal.ObjAttr {
	stat := info.Sys().(*syscall.Stat_t)
	attrs := &internal.ObjAttr{
		Path:  path,
		Name:  info.Name(),
		Size:  info.Size(),
		Mode:  info.Mode(),
		Mtime: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec),
		Atime: time.Unix(stat.Atim.Sec, stat.Atim.Nsec),
		Ctime: time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec),
	}

	if info.Mode()&os.ModeSymlink != 0 {
		attrs.Flags.Set(internal.PropFlagSymlink)
	} else if info.IsDir() {
		attrs.Flags.Set(internal.PropFlagIsDir)
	}

	return attrs
}

// ReadDir: Consolidate entries in storage and local cache to return the children under this path.
func (fc *FileCache) ReadDir(options internal.ReadDirOptions) ([]*internal.ObjAttr, error) {
	log.Trace("FileCache::ReadDir : %s", options.Name)

	// For read directory, there are three different child path situations we have to potentially handle.
	// 1. Path in storage but not in local cache
	// 2. Path not in storage but in local cache (this could happen if we recently created the file [and are currently writing to it]) (also supports immutable containers)
	// 3. Path in storage and in local cache (this could result in dirty properties on the service if we recently wrote to the file)

	// To cover case 1, grab all entries from storage
	attrs, err := fc.NextComponent().ReadDir(options)
	if err != nil {
		log.Err("FileCache::ReadDir : error fetching storage attributes [%s]", err.Error())
		// TODO : Should we return here if the directory failed to be read from storage?
	}

	// Create a mapping from path to index in the storage attributes array, so we can handle case 3 (conflicting attributes)
	var pathToIndex = make(map[string]int)
	for i, attr := range attrs {
		pathToIndex[attr.Path] = i
	}

	// To cover cases 2 and 3, grab entries from the local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	dirents, err := os.ReadDir(localPath)

	// If the local ReadDir fails it means the directory falls under case 1.
	// The directory will not exist locally even if it exists in the container
	// if the directory was freshly created or no files have been updated in the directory recently.
	if err == nil {
		// Enumerate over the results from the local cache and update/add to attrs to return if necessary (to support case 2 and 3)
		for _, entry := range dirents {
			entryPath := filepath.Join(options.Name, entry.Name())
			entryCachePath := filepath.Join(fc.tmpPath, entryPath)

			info, err := os.Stat(entryCachePath) // Grab local cache attributes
			// All directory operations are guaranteed to be synced with storage so they cannot be in a case 2 or 3 state.
			if err == nil && !info.IsDir() {
				idx, ok := pathToIndex[filepath.Join(options.Name, entry.Name())] // Grab the index of the corresponding storage attributes

				if ok { // Case 3 (file in storage and in local cache) so update the relevant attributes
					// Return from local cache only if file is not under download or deletion
					// If file is under download then taking size or mod time from it will be incorrect.
					if !fc.fileLocks.Locked(entryPath) {
						log.Debug("FileCache::ReadDir : updating %s from local cache", entryPath)
						attrs[idx].Size = info.Size()
						attrs[idx].Mtime = info.ModTime()
					}
				} else if !fc.createEmptyFile { // Case 2 (file only in local cache) so create a new attributes and add them to the storage attributes
					log.Debug("FileCache::ReadDir : serving %s from local cache", entryPath)
					attr := newObjAttr(entryPath, info)
					attrs = append(attrs, attr)
					pathToIndex[attr.Path] = len(attrs) - 1 // append adds to the end of an array
				}
			}
		}
	} else {
		log.Debug("FileCache::ReadDir : error fetching local attributes [%s]", err.Error())
	}

	return attrs, nil
}

// StreamDir : Add local files to the list retreived from storage container
func (fc *FileCache) StreamDir(options internal.StreamDirOptions) ([]*internal.ObjAttr, string, error) {
	attrs, token, err := fc.NextComponent().StreamDir(options)

	if token == "" {
		// This is the last set of objects retreived from container so we need to add local files here
		localPath := filepath.Join(fc.tmpPath, options.Name)
		dirents, err := os.ReadDir(localPath)

		if err == nil {
			// Enumerate over the results from the local cache and add to attrs
			for _, entry := range dirents {
				entryPath := filepath.Join(options.Name, entry.Name())
				entryCachePath := filepath.Join(fc.tmpPath, entryPath)

				info, err := os.Stat(entryCachePath) // Grab local cache attributes
				// If local file is not locked then ony use its attributes otherwise rely on container attributes
				if err == nil && !info.IsDir() &&
					!fc.fileLocks.Locked(entryPath) {

					// This is an overhead for streamdir for now
					// As list is paginated we have no way to know whether this particular item exists both in local cache
					// and container or not. So we rely on getAttr to tell if entry was cached then it exists in storage too
					// If entry does not exists on storage then only return a local item here.
					_, err := fc.NextComponent().GetAttr(internal.GetAttrOptions{Name: entryPath})
					if err != nil && (err == syscall.ENOENT || os.IsNotExist(err)) {
						log.Debug("FileCache::StreamDir : serving %s from local cache", entryPath)
						attr := newObjAttr(entryPath, info)
						attrs = append(attrs, attr)
					}
				}
			}
		}
	}

	return attrs, token, err
}

// IsDirEmpty: Whether or not the directory is empty
func (fc *FileCache) IsDirEmpty(options internal.IsDirEmptyOptions) bool {
	log.Trace("FileCache::IsDirEmpty : %s", options.Name)

	// If the directory does not exist locally then call the next component
	localPath := filepath.Join(fc.tmpPath, options.Name)
	f, err := os.Open(localPath)
	if os.IsNotExist(err) {
		log.Debug("FileCache::IsDirEmpty : %s not found in local cache", options.Name)
		return fc.NextComponent().IsDirEmpty(options)
	}

	if err != nil {
		log.Err("FileCache::IsDirEmpty : error opening directory %s [%s]", options.Name, err.Error())
		return false
	}

	// The file cache policy handles deleting locally empty directories in the cache
	// If the directory exists locally and is empty, it was probably recently emptied and we can trust this result.
	path, err := f.Readdirnames(1)
	if err == io.EOF {
		log.Debug("FileCache::IsDirEmpty : %s was empty in local cache", options.Name)
		return true
	}
	// If the local directory has a path in it, it is likely due to !createEmptyFile.
	if err == nil && !fc.createEmptyFile && len(path) > 0 {
		log.Debug("FileCache::IsDirEmpty : %s had a subpath in the local cache", options.Name)
		return false
	}

	return fc.NextComponent().IsDirEmpty(options)
}

// RenameDir: Recursively invalidate the source directory and its children
func (fc *FileCache) RenameDir(options internal.RenameDirOptions) error {
	log.Trace("FileCache::RenameDir : src=%s, dst=%s", options.Src, options.Dst)

	err := fc.NextComponent().RenameDir(options)
	if err != nil {
		log.Err("FileCache::RenameDir : error %s [%s]", options.Src, err.Error())
		return err
	}

	go fc.invalidateDirectory(options.Src)
	// TLDR: Dst is guaranteed to be non-existant or empty.
	// Note: We do not need to invalidate Dst due to the logic in our FUSE connector, see comments there.
	return nil
}

// CreateFile: Create the file in local cache.
func (fc *FileCache) CreateFile(options internal.CreateFileOptions) (*handlemap.Handle, error) {
	//defer exectime.StatTimeCurrentBlock("FileCache::CreateFile")()
	log.Trace("FileCache::CreateFile : name=%s, mode=%d", options.Name, options.Mode)

	fc.fileLocks.Lock(options.Name)
	defer fc.fileLocks.Unlock(options.Name)

	// createEmptyFile was added to optionally support immutable containers. If customers do not care about immutability they can set this to true.
	if fc.createEmptyFile {
		// We tried moving CreateFile to a seperate thread for better perf.
		// However, before it is created in storage, if GetAttr is called, the call will fail since the file
		// does not exist in storage yet, failing the whole CreateFile sequence in FUSE.
		_, err := fc.NextComponent().CreateFile(options)
		if err != nil {
			log.Err("FileCache::CreateFile : Failed to create file %s", options.Name)
			return nil, err
		}
	}

	// Create the file in local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	fc.policy.CacheValid(localPath)

	err := os.MkdirAll(filepath.Dir(localPath), fc.defaultPermission)
	if err != nil {
		log.Err("FileCache::CreateFile : unable to create local directory %s [%s]", options.Name, err.Error())
		return nil, err
	}

	// Open the file and grab a shared lock to prevent deletion by the cache policy.
	f, err := os.OpenFile(localPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, options.Mode)
	if err != nil {
		log.Err("FileCache::CreateFile : error opening local file %s [%s]", options.Name, err.Error())
		return nil, err
	}
	// The user might change permissions WHILE creating the file therefore we need to account for that
	if options.Mode != common.DefaultFilePermissionBits {
		fc.missedChmodList.LoadOrStore(options.Name, true)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err != nil {
		log.Err("FileCache::CreateFile : error flocking %s [%s]", options.Name, err.Error())
		return nil, err
	}

	handle := handlemap.NewHandle(options.Name)
	handle.SetFileObject(f)

	if fc.directRead {
		handle.Flags.Set(handlemap.HandleFlagCached)
	}

	// If an empty file is created in storage then there is no need to upload if FlushFile is called immediatly after CreateFile.
	if !fc.createEmptyFile {
		handle.Flags.Set(handlemap.HandleFlagDirty)
	}

	return handle, nil
}

// Validate that storage 404 errors truly correspond to Does Not Exist.
// path: the storage path
// err: the storage error
// method: the caller method name
// recoverable: whether or not case 2 is recoverable on flush/close of the file
func (fc *FileCache) validateStorageError(path string, err error, method string, recoverable bool) error {
	// For methods that take in file name, the goal is to update the path in storage and the local cache.
	// See comments in GetAttr for the different situations we can run into. This specifically handles case 2.
	if err != nil {
		if err == syscall.ENOENT || os.IsNotExist(err) {
			log.Debug("FileCache::%s : %s does not exist in storage", method, path)
			if !fc.createEmptyFile {
				// Check if the file exists in the local cache
				// (policy might not think the file exists if the file is merely marked for evication and not actually evicted yet)
				localPath := filepath.Join(fc.tmpPath, path)
				_, err := os.Stat(localPath)
				if os.IsNotExist(err) { // If the file is not in the local cache, then the file does not exist.
					log.Err("FileCache::%s : %s does not exist in local cache", method, path)
					return syscall.ENOENT
				} else {
					if !recoverable {
						log.Err("FileCache::%s : %s has not been closed/flushed yet, unable to recover this operation on close", method, path)
						return syscall.EIO
					} else {
						log.Info("FileCache::%s : %s has not been closed/flushed yet, we can recover this operation on close", method, path)
						return nil
					}
				}
			}
		} else {
			return err
		}
	}
	return nil
}

// DeleteFile: Invalidate the file in local cache.
func (fc *FileCache) DeleteFile(options internal.DeleteFileOptions) error {
	log.Trace("FileCache::DeleteFile : name=%s", options.Name)

	fc.fileLocks.Lock(options.Name)
	defer fc.fileLocks.Unlock(options.Name)

	err := fc.NextComponent().DeleteFile(options)
	err = fc.validateStorageError(options.Name, err, "DeleteFile", false)
	if err != nil {
		log.Err("FileCache::DeleteFile : error  %s [%s]", options.Name, err.Error())
		return err
	}

	localPath := filepath.Join(fc.tmpPath, options.Name)
	os.Remove(localPath)
	fc.policy.CachePurge(localPath)
	return nil
}

// isDownloadRequired: Whether or not the file needs to be downloaded to local cache.
func (fc *FileCache) isDownloadRequired(localPath string) (bool, bool) {
	fileExists := false
	downloadRequired := false

	// The file is not cached
	if !fc.policy.IsCached(localPath) {
		log.Debug("FileCache::isDownloadRequired : %s not present in local cache policy", localPath)
		downloadRequired = true
	}

	finfo, err := os.Stat(localPath)
	if err == nil {
		// The file exists in local cache
		// The file needs to be downloaded if the cacheTimeout elapsed (check last change time and last modified time)
		fileExists = true
		stat := finfo.Sys().(*syscall.Stat_t)

		// Deciding based on last modified time is not correct. Last modified time is based on the file was last written
		// so if file was last written back to container 2 days back then even downloading it now shall represent the same date
		// hence immediatly after download it will become invalid. It shall be based on when the file was last downloaded.
		// We can rely on last change time because once file is downloaded we reset its last mod time (represent same time as
		// container on the local disk by resetting last mod time of local disk with utimens)
		// and hence last change time on local disk will then represent the download time.

		if time.Since(finfo.ModTime()).Seconds() > fc.cacheTimeout &&
			time.Since(time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)).Seconds() > fc.cacheTimeout {
			log.Debug("FileCache::isDownloadRequired : %s not valid as per time checks", localPath)
			downloadRequired = true
		}
	} else if os.IsNotExist(err) {
		// The file does not exist in the local cache so it needs to be downloaded
		log.Debug("FileCache::isDownloadRequired : %s not present in local cache", localPath)
		downloadRequired = true
	} else {
		// Catch all, the file needs to be downloaded
		log.Debug("FileCache::isDownloadRequired : error calling stat %s [%s]", localPath, err.Error())
		downloadRequired = true
	}

	return downloadRequired, fileExists
}

// OpenFile: Makes the file available in the local cache for further file operations.
func (fc *FileCache) OpenFile(options internal.OpenFileOptions) (*handlemap.Handle, error) {
	log.Trace("FileCache::OpenFile : name=%s, flags=%d, mode=%s", options.Name, options.Flags, options.Mode)

	localPath := filepath.Join(fc.tmpPath, options.Name)
	var f *os.File
	var err error

	fc.fileLocks.Lock(options.Name)
	defer fc.fileLocks.Unlock(options.Name)

	fc.policy.CacheValid(localPath)

	downloadRequired, fileExists := fc.isDownloadRequired(localPath)

	if fileExists && downloadRequired {
		// If the file exists, check whether the file is free to be overwritten or not
		// If the lock is held then we cannot update the file
		f, err = os.OpenFile(localPath, os.O_WRONLY, options.Mode)
		if err != nil {
			if os.IsPermission(err) {
				// File is not having write permission, for re-download we need that permissions
				log.Err("FileCache::OpenFile : %s failed to open in write mode", localPath)
				f, err = os.OpenFile(localPath, os.O_RDONLY, os.FileMode(0666))
				if err == nil {
					// Able to open file in readonly mode then reset the permissions
					err = f.Chmod(os.FileMode(0666))
					if err != nil {
						log.Err("FileCache::OpenFile : Failed to reset permissions for %s", localPath)
						return nil, err
					}
					f.Close()

					// Retry open in write mode now
					f, err = os.OpenFile(localPath, os.O_WRONLY, options.Mode)
					if err != nil {
						log.Err("FileCache::OpenFile : Failed to re-open file in write mode %s", localPath)
						return nil, err
					}
				} else {
					log.Err("FileCache::OpenFile : Failed to open file in read mode %s", localPath)
					return nil, err
				}
			} else {
				log.Err("FileCache::OpenFile : Failed to open file %s in WR mode", localPath)
				return nil, err
			}
		}

		// Grab an exclusive lock to prevent anyone from touching the file while we write to it
		exLocked := false
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != nil {
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				log.Err("FileCache::OpenFile : error flocking %s(%d) [%s]", localPath, int(f.Fd()), err.Error())
				return nil, err
			} else {
				// This indicates that the file is already open, so we can skip the re-download here.
				// This may happen if we decided to download based on cache timeouts but someone may be accessing the file.
				log.Err("FileCache::OpenFile : error flocking %s(%d) [%s], proceeding with existing cached copy", localPath, int(f.Fd()), err.Error())
				downloadRequired = false
			}
		} else {
			exLocked = true
		}

		if exLocked {
			// To prepare for re-download, delete the existing file.
			if downloadRequired {
				err = os.Remove(localPath)
				if err != nil {
					log.Err("FileCache::OpenFile : error removing %s [%s]", localPath, err.Error())
				}
			}

			err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Unlock the exclusive lock
			if err != nil {
				log.Err("FileCache::OpenFile : error unlocking fd %s(%d) [%s]", localPath, int(f.Fd()), err.Error())
				return nil, err
			}
		}
		exLocked = false
		f.Close()
	}

	if downloadRequired {
		log.Debug("FileCache::OpenFile : Need to re-download %s", options.Name)

		// Create the file if if doesn't already exist.
		if !fileExists {
			err := os.MkdirAll(filepath.Dir(localPath), fc.defaultPermission)
			if err != nil {
				log.Err("FileCache::OpenFile : error creating directory structure for file %s [%s]", options.Name, err.Error())
				return nil, err
			}
		}

		// Open the file in write mode.
		f, err = os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY, options.Mode)
		if err != nil {
			log.Err("FileCache::OpenFile : error creating new file %s [%s]", options.Name, err.Error())
			return nil, err
		}

		attrReceived := false
		fileSize := int64(0)
		attr, err := fc.NextComponent().GetAttr(internal.GetAttrOptions{Name: options.Name})
		if err != nil {
			log.Err("FileCache::OpenFile : Failed to get attr of %s [%s]", options.Name, err.Error())
		} else {
			attrReceived = true
			fileSize = int64(attr.Size)
		}

		if !attrReceived || fileSize > 0 {
			// Download/Copy the file from storage to the local file.
			err = fc.NextComponent().CopyToFile(
				internal.CopyToFileOptions{
					Name:   options.Name,
					Offset: 0,
					Count:  fileSize,
					File:   f,
				})
			if err != nil {
				log.Err("FileCache::OpenFile : error downloading file from storage %s [%s]", options.Name, err.Error())
				return nil, err
			}
		}

		log.Debug("FileCache::OpenFile : Download of %s is complete", options.Name)
		f.Close()

		// TODO: GO SDK should have some way to return attr on download to file since they do that call anyway, this will save us an extra call.
		// However this can only be done once ADLS accounts have migrated the Download API to the blob endpoint so we can get mode.

		// After downloading the file, update the modified times and mode of the file.
		fileMode := fc.defaultPermission
		if attrReceived && !attr.IsModeDefault() {
			fileMode = attr.Mode
		}

		// If user has selected some non default mode in config then every local file shall be created with that mode only
		err = os.Chmod(localPath, fileMode)
		if err != nil {
			log.Err("FileCache::OpenFile : Failed to change mode of file %s [%s]", options.Name, err.Error())
		}
		// TODO: When chown is supported should we update that?

		// chtimes shall be the last api otherwise calling chmod/chown will update the last change time
		err = os.Chtimes(localPath, attr.Atime, attr.Mtime)
		if err != nil {
			log.Err("FileCache::OpenFile : Failed to change times of file %s [%s]", options.Name, err.Error())
		}
	} else {
		log.Debug("FileCache::OpenFile : %s will be served from cache", options.Name)
	}

	// Open the file and grab a shared lock to prevent deletion by the cache policy.
	f, err = os.OpenFile(localPath, options.Flags, options.Mode)
	if err != nil {
		log.Err("FileCache::OpenFile : error opening cached file %s [%s]", options.Name, err.Error())
		return nil, err
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err != nil {
		log.Err("FileCache::OpenFile : error flocking %s [%s]", options.Name, err.Error())
		return nil, err
	}

	handle := handlemap.NewHandle(options.Name)
	inf, err := f.Stat()
	if err == nil {
		handle.Size = inf.Size()
	}
	handle.SetFileObject(f)

	if fc.directRead {
		handle.Flags.Set(handlemap.HandleFlagCached)
	}

	log.Info("FileCache::OpenFile : file=%s, fd=%d", options.Name, f.Fd())

	return handle, nil
}

// CloseFile: Flush the file and invalidate it from the cache.
func (fc *FileCache) CloseFile(options internal.CloseFileOptions) error {
	log.Trace("FileCache::CloseFile : name=%s, handle=%d", options.Handle.Path, options.Handle.ID)

	localPath := filepath.Join(fc.tmpPath, options.Handle.Path)

	if options.Handle.Dirty() {
		log.Info("FileCache::CloseFile : name=%s, handle=%d dirty. Flushing the file.", options.Handle.Path, options.Handle.ID)
		err := fc.FlushFile(internal.FlushFileOptions{Handle: options.Handle})
		if err != nil {
			log.Err("FileCache::CloseFile : failed to flush file %s", options.Handle.Path)
			return err
		}
	}

	f := options.Handle.GetFileObject()
	if f == nil {
		log.Err("FileCache::CloseFile : error [missing fd in handle object] %s", options.Handle.Path)
		return syscall.EBADF
	}

	err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Unlock any locks held
	if err != nil {
		log.Err("FileCache::CloseFile : error unlocking fd %s(%d) [%s]", options.Handle.Path, int(f.Fd()), err.Error())
		return err
	}

	err = f.Close()
	if err != nil {
		log.Err("FileCache::CloseFile : error closing file %s(%d) [%s]", options.Handle.Path, int(f.Fd()), err.Error())
		return err
	}

	// If it is an fsync op then purge the file
	if options.Handle.Fsynced() {
		log.Trace("FileCache::CloseFile : fsync/sync op, purging %s", options.Handle.Path)

		fc.fileLocks.Lock(options.Handle.Path)
		defer fc.fileLocks.Unlock(options.Handle.Path)

		localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
		os.Remove(localPath)
		fc.policy.CachePurge(localPath)
		return nil
	}

	fc.policy.CacheInvalidate(localPath) // Invalidate the file from the local cache.
	return nil
}

// ReadFile: Read the local file
func (fc *FileCache) ReadFile(options internal.ReadFileOptions) ([]byte, error) {
	// The file should already be in the cache since CreateFile/OpenFile was called before and a shared lock was acquired.
	localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
	fc.policy.CacheValid(localPath)

	f := options.Handle.GetFileObject()
	if f == nil {
		log.Err("FileCache::ReadFile : error [couldn't find fd in handle] %s", options.Handle.Path)
		return nil, syscall.EBADF
	}

	// Get file info so we know the size of data we expect to read.
	info, err := f.Stat()
	if err != nil {
		log.Err("FileCache::ReadFile : error stat %s [%s] ", options.Handle.Path, err.Error())
		return nil, err
	}
	data := make([]byte, info.Size())
	bytesRead, err := f.Read(data)

	if int64(bytesRead) != info.Size() {
		log.Err("FileCache::ReadFile : error [couldn't read entire file] %s", options.Handle.Path)
		return nil, syscall.EIO
	}

	return data, err
}

// ReadInBuffer: Read the local file into a buffer
func (fc *FileCache) ReadInBuffer(options internal.ReadInBufferOptions) (int, error) {
	//defer exectime.StatTimeCurrentBlock("FileCache::ReadInBuffer")()
	// The file should already be in the cache since CreateFile/OpenFile was called before and a shared lock was acquired.
	localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
	fc.policy.CacheValid(localPath)

	f := options.Handle.GetFileObject()
	if f == nil {
		log.Err("FileCache::ReadInBuffer : error [couldn't find fd in handle] %s", options.Handle.Path)
		return 0, syscall.EBADF
	}

	return f.ReadAt(options.Data, options.Offset)
}

// WriteFile: Write to the local file
func (fc *FileCache) WriteFile(options internal.WriteFileOptions) (int, error) {
	//defer exectime.StatTimeCurrentBlock("FileCache::WriteFile")()

	// The file should already be in the cache since CreateFile/OpenFile was called before and a shared lock was acquired.
	localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
	fc.policy.CacheValid(localPath)

	f := options.Handle.GetFileObject()
	if f == nil {
		log.Err("FileCache::WriteFile : error [couldn't find fd in handle] %s", options.Handle.Path)
		return 0, syscall.EBADF
	}

	options.Handle.Flags.Set(handlemap.HandleFlagDirty) // Mark the handle dirty so the file is written back to storage on FlushFile.

	return f.WriteAt(options.Data, options.Offset)
}

func (fc *FileCache) SyncFile(options internal.SyncFileOptions) error {
	err := fc.NextComponent().SyncFile(options)
	if err != nil {
		log.Err("FileCache::SyncFile : %s failed", options.Handle.Path)
		return err
	}

	options.Handle.Flags.Set(handlemap.HandleFlagFSynced)
	return err
}

// in SyncDir we're not going to clear the file cache for now
// on regular linux its fs responsibility
// func (fc *FileCache) SyncDir(options internal.SyncDirOptions) error {
// 	log.Trace("FileCache::SyncDir : %s", options.Name)

// 	err := fc.NextComponent().SyncDir(options)
// 	if err != nil {
// 		log.Err("FileCache::SyncDir : %s failed", options.Name)
// 		return err
// 	}
// 	// TODO: we can decide here if we want to flush all the files in the directory first or not. Currently I'm just invalidating files
// 	// within the dir
// 	go fc.invalidateDirectory(options.Name)
// 	return nil
// }

// FlushFile: Flush the local file to storage
func (fc *FileCache) FlushFile(options internal.FlushFileOptions) error {
	//defer exectime.StatTimeCurrentBlock("FileCache::FlushFile")()
	log.Trace("FileCache::FlushFile : handle=%d, path=%s", options.Handle.ID, options.Handle.Path)

	// The file should already be in the cache since CreateFile/OpenFile was called before and a shared lock was acquired.
	localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
	fc.policy.CacheValid(localPath)
	// if our handle is dirty then that means we wrote to the file
	if options.Handle.Dirty() {
		f := options.Handle.GetFileObject()
		if f == nil {
			log.Err("FileCache::FlushFile : error [couldn't find fd in handle] %s", options.Handle.Path)
			return syscall.EBADF
		}

		// Flush all data to disk that has been buffered by the kernel.
		// We cannot close the incoming handle since the user called flush, note close and flush can be called on the same handle multiple times.
		// To ensure the data is flushed to disk before writing to storage, we duplicate the handle and close that handle.
		dupFd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			log.Err("FileCache::FlushFile : error [couldn't duplicate the fd] %s", options.Handle.Path)
			return syscall.EIO
		}

		err = syscall.Close(dupFd)
		if err != nil {
			log.Err("FileCache::FlushFile : error [unable to close duplicate fd] %s", options.Handle.Path)
			return syscall.EIO
		}

		// Write to storage
		// Create a new handle for the SDK to use to upload (read local file)
		// The local handle can still be used for read and write.
		fc.fileLocks.Lock(options.Handle.Path)
		defer fc.fileLocks.Unlock(options.Handle.Path)

		uploadHandle, err := os.Open(localPath)
		if err != nil {
			options.Handle.Flags.Clear(handlemap.HandleFlagDirty)
			log.Err("FileCache::FlushFile : error [unable to open upload handle] %s [%s]", options.Handle.Path, err.Error())
			return nil
		}

		err = fc.NextComponent().CopyFromFile(
			internal.CopyFromFileOptions{
				Name: options.Handle.Path,
				File: uploadHandle,
			})
		if err != nil {
			uploadHandle.Close()
			log.Err("FileCache::FlushFile : %s upload failed [%s]", options.Handle.Path, err.Error())
			return err
		}
		options.Handle.Flags.Clear(handlemap.HandleFlagDirty)
		uploadHandle.Close()

		// If chmod was done on the file before it was uploaded to container then setting up mode would have been missed
		// Such file names are added to this map and here post upload we try to set the mode correctly
		_, found := fc.missedChmodList.Load(options.Handle.Path)
		if found {
			// If file is found in map it means last chmod was missed on this
			// Delete the entry from map so that any further flush do not try to update the mode again
			fc.missedChmodList.Delete(options.Handle.Path)

			// When chmod on container was missed, local file was updated with correct mode
			// Here take the mode from local cache and update the container accordingly
			localPath := filepath.Join(fc.tmpPath, options.Handle.Path)
			info, err := os.Lstat(localPath)
			if err == nil {
				err = fc.Chmod(internal.ChmodOptions{Name: options.Handle.Path, Mode: info.Mode()})
				if err != nil {
					// chmod was missed earlier for this file and doing it now also
					// resulted in error so ignore this one and proceed for flush handling
					log.Err("FileCache::FlushFile : %s chmod failed [%s]", options.Handle.Path, err.Error())
				}
			}
		}
	}

	return nil
}

// GetAttr: Consolidate attributes from storage and local cache
func (fc *FileCache) GetAttr(options internal.GetAttrOptions) (*internal.ObjAttr, error) {
	log.Trace("FileCache::GetAttr : %s", options.Name)

	// For get attr, there are three different path situations we have to potentially handle.
	// 1. Path in storage but not in local cache
	// 2. Path not in storage but in local cache (this could happen if we recently created the file [and are currently writing to it]) (also supports immutable containers)
	// 3. Path in storage and in local cache (this could result in dirty properties on the service if we recently wrote to the file)

	// To cover case 1, get attributes from storage
	var exists bool
	attrs, err := fc.NextComponent().GetAttr(options)
	if err != nil {
		if err == syscall.ENOENT || os.IsNotExist(err) {
			log.Debug("FileCache::GetAttr : %s does not exist in storage", options.Name)
			exists = false
		} else {
			log.Err("FileCache::GetAttr : Failed to get attr of %s [%s]", options.Name, err.Error())
			return &internal.ObjAttr{}, err
		}
	} else {
		exists = true
	}

	// To cover cases 2 and 3, grab the attributes from the local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	info, err := os.Lstat(localPath)
	// All directory operations are guaranteed to be synced with storage so they cannot be in a case 2 or 3 state.
	if (err == nil || os.IsExist(err)) && !info.IsDir() {
		if exists { // Case 3 (file in storage and in local cache) so update the relevant attributes
			// Return from local cache only if file is not under download or deletion
			// If file is under download then taking size or mod time from it will be incorrect.
			if !fc.fileLocks.Locked(options.Name) {
				log.Debug("FileCache::GetAttr : updating %s from local cache", options.Name)
				attrs.Size = info.Size()
				attrs.Mtime = info.ModTime()
			} else {
				log.Debug("FileCache::GetAttr : %s is locked, use storage attributes", options.Name)
			}
		} else { // Case 2 (file only in local cache) so create a new attributes and add them to the storage attributes
			if !strings.Contains(localPath, fc.tmpPath) {
				// Here if the path is going out of the temp directory then return ENOENT
				exists = false
			} else {
				log.Debug("FileCache::GetAttr : serving %s attr from local cache", options.Name)
				exists = true
				attrs = newObjAttr(options.Name, info)
			}
		}
	}

	if !exists {
		return &internal.ObjAttr{}, syscall.ENOENT
	}

	return attrs, nil
}

// RenameFile: Invalidate the file in local cache.
func (fc *FileCache) RenameFile(options internal.RenameFileOptions) error {
	log.Trace("FileCache::RenameFile : src=%s, dst=%s", options.Src, options.Dst)

	fc.fileLocks.Lock(options.Src)
	defer fc.fileLocks.Unlock(options.Src)

	fc.fileLocks.Lock(options.Dst)
	defer fc.fileLocks.Unlock(options.Dst)

	err := fc.NextComponent().RenameFile(options)
	err = fc.validateStorageError(options.Src, err, "RenameFile", false)
	if err != nil {
		log.Err("FileCache::RenameFile : %s failed to rename file [%s]", options.Src, err.Error())
		return err
	}

	localSrcPath := filepath.Join(fc.tmpPath, options.Src)
	localDstPath := filepath.Join(fc.tmpPath, options.Dst)

	// in case of git clone multiple rename requests come for which destination files already exists in system
	// if we do not perform rename operation locally and those destination files are cached then next time they are read
	// we will be serving the wrong content (as we did not rename locally, we still be having older destination files with
	// stale content). We either need to remove dest file as well from cache or just run rename to replace the content.
	err = os.Rename(localSrcPath, localDstPath)
	if err != nil {
		os.Remove(localDstPath)
		fc.policy.CachePurge(localDstPath)
		log.Err("FileCache::RenameFile : %s failed to rename local file [%s]", options.Src, err.Error())
	}

	os.Remove(localSrcPath)
	fc.policy.CachePurge(localSrcPath)

	return nil
}

// TruncateFile: Update the file with its new size.
func (fc *FileCache) TruncateFile(options internal.TruncateFileOptions) error {
	log.Trace("FileCache::TruncateFile : name=%s, size=%d", options.Name, options.Size)

	fc.fileLocks.Lock(options.Name)
	defer fc.fileLocks.Unlock(options.Name)

	err := fc.NextComponent().TruncateFile(options)
	err = fc.validateStorageError(options.Name, err, "TruncateFile", true)
	if err != nil {
		log.Err("FileCache::TruncateFile : %s failed to truncate [%s]", options.Name, err.Error())
		return err
	}

	// Update the size of the file in the local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	info, err := os.Stat(localPath)
	if err == nil || os.IsExist(err) {
		fc.policy.CacheValid(localPath)

		if info.Size() != options.Size {
			err = os.Truncate(localPath, options.Size)
			if err != nil {
				log.Err("FileCache::TruncateFile : error truncating cached file %s [%s]", localPath, err.Error())
				return err
			}
		}
	}

	return nil
}

// Chmod : Update the file with its new permissions
func (fc *FileCache) Chmod(options internal.ChmodOptions) error {
	log.Trace("FileCache::Chmod : Change mode of path %s", options.Name)

	// Update the file in storage
	err := fc.NextComponent().Chmod(options)
	err = fc.validateStorageError(options.Name, err, "Chmod", false)
	if err != nil {
		if err != syscall.EIO {
			log.Err("FileCache::Chmod : %s failed to change mode [%s]", options.Name, err.Error())
			return err
		} else {
			fc.missedChmodList.LoadOrStore(options.Name, true)
		}
	}

	// Update the mode of the file in the local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	info, err := os.Stat(localPath)
	if err == nil || os.IsExist(err) {
		fc.policy.CacheValid(localPath)

		if info.Mode() != options.Mode {
			err = os.Chmod(localPath, options.Mode)
			if err != nil {
				log.Err("FileCache::Chmod : error changing mode on the cached path %s [%s]", localPath, err.Error())
				return err
			}
		}
	}

	return nil
}

// Chown : Update the file with its new owner and group
func (fc *FileCache) Chown(options internal.ChownOptions) error {
	log.Trace("FileCache::Chown : Change owner of path %s", options.Name)

	// Update the file in storage
	err := fc.NextComponent().Chown(options)
	err = fc.validateStorageError(options.Name, err, "Chown", false)
	if err != nil {
		log.Err("FileCache::Chown : %s failed to change owner [%s]", options.Name, err.Error())
		return err
	}

	// Update the owner and group of the file in the local cache
	localPath := filepath.Join(fc.tmpPath, options.Name)
	_, err = os.Stat(localPath)
	if err == nil || os.IsExist(err) {
		fc.policy.CacheValid(localPath)

		err = os.Chown(localPath, options.Owner, options.Group)
		if err != nil {
			log.Err("FileCache::Chown : error changing owner on the cached path %s [%s]", localPath, err.Error())
			return err
		}
	}

	return nil
}

// ------------------------- Factory -------------------------------------------

// Pipeline will call this method to create your object, initialize your variables here
// << DO NOT DELETE ANY AUTO GENERATED CODE HERE >>
func NewFileCacheComponent() internal.Component {
	comp := &FileCache{
		fileLocks: common.NewLockMap(),
	}
	comp.SetName(compName)
	config.AddConfigChangeEventListener(comp)
	return comp
}

// On init register this component to pipeline and supply your constructor
func init() {
	internal.AddComponent(compName, NewFileCacheComponent)
	tmpPathFlag := config.AddStringFlag("tmp-path", "", "Configures the tmp location for the cache. Configure the fastest disk (SSD or ramdisk) for best performance.")
	config.BindPFlag(compName+".path", tmpPathFlag)
	config.RegisterFlagCompletionFunc("tmp-path", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	})
}