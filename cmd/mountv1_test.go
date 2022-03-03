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

package cmd

import (
	"blobfuse2/common"
	"blobfuse2/common/config"
	"blobfuse2/common/log"
	"blobfuse2/component/attr_cache"
	"blobfuse2/component/azstorage"
	"blobfuse2/component/file_cache"
	"blobfuse2/component/stream"
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type generateConfigTestSuite struct {
	suite.Suite
	assert *assert.Assertions
}

func (suite *generateConfigTestSuite) SetupTest() {
	suite.assert = assert.New(suite.T())
	err := log.SetDefaultLogger("silent", common.LogConfig{Level: common.ELogLevel.LOG_DEBUG()})
	if err != nil {
		panic("Unable to set silent logger as default.")
	}
}

func (suite *generateConfigTestSuite) cleanupTest() {
	resetCLIFlags(*generateConfigCmd)
}

// Taken from cobra library's testing https://github.com/spf13/cobra/blob/master/command_test.go#L34
func executeCommandC(root *cobra.Command, args ...string) (output string, err error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err = root.Execute()

	return buf.String(), err
}

func resetCLIFlags(cmd cobra.Command) {
	// reset all CLI flags before next test
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		f.Changed = false
	})
}

func TestGenerateConfig(t *testing.T) {
	suite.Run(t, new(generateConfigTestSuite))
}

func (suite *generateConfigTestSuite) TestHelp() {
	defer suite.cleanupTest()
	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", "-h")
	suite.assert.Nil(err)
}

func randomString(length int) string {
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, length)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[:length]
}

func generateFileName() string {
	return "file" + randomString(8)
}

func (suite *generateConfigTestSuite) TestConfigFileInvalid() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountName myAccountName myOtherAccountName")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.NotNil(err)
}

func (suite *generateConfigTestSuite) TestConfigFileKey() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountName myAccountName\naccountKey myAccountKey\nauthType Key\ncontainerName myContainerName\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("myAccountName", options.AccountName)
	suite.assert.EqualValues("myAccountKey", options.AccountKey)
	suite.assert.EqualValues("key", options.AuthMode)
	suite.assert.EqualValues("myContainerName", options.Container)
	suite.assert.EqualValues("https://myAccountName.blob.core.windows.net", options.Endpoint)
}

func (suite *generateConfigTestSuite) TestConfigFileSas() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountName myAccountName\nsasToken mySasToken\nauthType SAS\ncontainerName myContainerName\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("myAccountName", options.AccountName)
	suite.assert.EqualValues("mySasToken", options.SaSKey)
	suite.assert.EqualValues("sas", options.AuthMode)
	suite.assert.EqualValues("myContainerName", options.Container)
	suite.assert.EqualValues("https://myAccountName.blob.core.windows.net", options.Endpoint)
}

func (suite *generateConfigTestSuite) TestConfigFileSPN() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountName myAccountName\nservicePrincipalClientId clientId\nservicePrincipalTenantId tenantId\nservicePrincipalClientSecret clientSecret\naadEndpoint aadEndpoint\nauthType SPN\ncontainerName myContainerName\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("myAccountName", options.AccountName)
	suite.assert.EqualValues("clientId", options.ClientID)
	suite.assert.EqualValues("tenantId", options.TenantID)
	suite.assert.EqualValues("clientSecret", options.ClientSecret)
	suite.assert.EqualValues("aadEndpoint", options.ActiveDirectoryEndpoint)
	suite.assert.EqualValues("spn", options.AuthMode)
	suite.assert.EqualValues("myContainerName", options.Container)
	suite.assert.EqualValues("https://myAccountName.blob.core.windows.net", options.Endpoint)
}
func (suite *generateConfigTestSuite) TestConfigFileMSI() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountName myAccountName\nidentityClientId clientId\nidentityObjectId objectId\nidentityResourceId resourceId\nauthType MSI\ncontainerName myContainerName\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("myAccountName", options.AccountName)
	suite.assert.EqualValues("clientId", options.ApplicationID)
	suite.assert.EqualValues("objectId", options.ObjectID)
	suite.assert.EqualValues("resourceId", options.ResourceID)
	suite.assert.EqualValues("msi", options.AuthMode)
	suite.assert.EqualValues("myContainerName", options.Container)
	suite.assert.EqualValues("https://myAccountName.blob.core.windows.net", options.Endpoint)
}

func (suite *generateConfigTestSuite) TestConfigFileProxy() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("httpProxy httpProxy\nhttpsProxy httpsProxy\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("httpProxy", options.HttpProxyAddress)
	suite.assert.EqualValues("httpsProxy", options.HttpsProxyAddress)
}

func (suite *generateConfigTestSuite) TestConfigFileBlobEndpoint() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("blobEndpoint blobEndpoint\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("blobEndpoint", options.Endpoint)
}

func (suite *generateConfigTestSuite) TestConfigFileAccountType() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("accountType adls\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("adls", options.AccountType)
	suite.assert.EqualValues("https://.dfs.core.windows.net", options.Endpoint)
}

func (suite *generateConfigTestSuite) TestConfigFileAuthMode() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("authType Key\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("key", options.AuthMode)
}

func (suite *generateConfigTestSuite) TestConfigFileLogLevel() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("logLevel LOG_ERROR\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := LogOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("logging", &options)

	suite.assert.EqualValues("LOG_ERROR", options.LogLevel)
}

func (suite *generateConfigTestSuite) TestConfigFileIgnoreCommentsNewLine() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("logLevel LOG_ERROR\n# accountName myAccountName\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := LogOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("logging", &options)

	suite.assert.EqualValues("LOG_ERROR", options.LogLevel)
}

func (suite *generateConfigTestSuite) TestConfigFileIgnoreCommentsSameLine() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("logLevel LOG_ERROR #LOG_DEBUG\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := LogOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("logging", &options)

	suite.assert.EqualValues("LOG_ERROR", options.LogLevel)
}

func (suite *generateConfigTestSuite) TestConfigFileCaCertFileError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("caCertFile caCertFile\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.NotNil(err)
}

func (suite *generateConfigTestSuite) TestConfigFileDnsTypeError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("dnsType dnsType\n")

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.NotNil(err)
}

func (suite *generateConfigTestSuite) TestConfigCLILogLevel() {
	defer suite.cleanupTest()
	name := generateFileName()
	v1ConfigFile, _ := ioutil.TempFile("", name+".tmp.cfg")
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v1ConfigFile.Name())
	defer os.Remove(v2ConfigFile.Name())
	v1ConfigFile.WriteString("logLevel LOG_ERROR\n")
	logLevel := "--log-level=LOG_DEBUG"
	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", logLevel, fmt.Sprintf("--output-file=%s", v2ConfigFile.Name()), fmt.Sprintf("--config-file=%s", v1ConfigFile.Name()))
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := LogOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("logging", &options)

	suite.assert.EqualValues("LOG_DEBUG", options.LogLevel)
}

func (suite *generateConfigTestSuite) TestCLIParamLogging() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	logLevel := "--log-level=LOG_DEBUG"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, logLevel)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := LogOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("logging", &options)

	suite.assert.EqualValues("LOG_DEBUG", options.LogLevel)
}

func (suite *generateConfigTestSuite) TestCLIParamFileCache() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	tmpPath := "--tmp-path=fileCachePath"
	size := "--cache-size-mb=15"
	timeout := "--file-cache-timeout-in-seconds=60"
	maxEviction := "--max-eviction=7"
	high := "--high-disk-threshold=60"
	low := "--low-disk-threshold=40"
	emptyDirCheck := "--empty-dir-check=false"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, tmpPath, size, timeout, maxEviction, high, low, emptyDirCheck)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := file_cache.FileCacheOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("file_cache", &options)

	suite.assert.EqualValues("fileCachePath", options.TmpPath)
	suite.assert.EqualValues(15, options.MaxSizeMB)
	suite.assert.EqualValues(60, options.Timeout)
	suite.assert.EqualValues(7, options.MaxEviction)
	suite.assert.EqualValues(60, options.HighThreshold)
	suite.assert.EqualValues(40, options.LowThreshold)
	suite.assert.True(options.AllowNonEmpty)
}

func (suite *generateConfigTestSuite) TestAddStreamAndFileCache() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	tmpPath := "--tmp-path=fileCachePath"
	size := "--cache-size-mb=15"
	timeout := "--file-cache-timeout-in-seconds=60"
	useStreaming := "--streaming=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, tmpPath, size, timeout, useStreaming)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := mountOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.Unmarshal(&options)
	suite.assert.EqualValues([]string{"libfuse", "stream", "azstorage"}, options.Components)

}

func (suite *generateConfigTestSuite) TestComponentCorrectOrder() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	tmpPath := "--tmp-path=fileCachePath"
	size := "--cache-size-mb=15"
	timeout := "--file-cache-timeout-in-seconds=60"
	useAttrCache := "--use-attr-cache"
	streaming := "--streaming=false"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, tmpPath, size, timeout, useAttrCache, streaming)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := mountOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.Unmarshal(&options)
	suite.assert.EqualValues([]string{"libfuse", "file_cache", "attr_cache", "azstorage"}, options.Components)
}

func (suite *generateConfigTestSuite) TestCLIParamFileCacheUploadModifiedOnlyError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	modifiedOnly := "--upload-modified-only=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, modifiedOnly)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamFileCachePollTimeoutError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	modifiedOnly := "--cache-poll-timeout-msec=60"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, modifiedOnly)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamStreaming() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	streaming := "--streaming=true"
	blockSize := "--block-size-mb=5"
	blocksPerFile := "--max-blocks-per-file=10"
	cacheSize := "--stream-cache-mb=40"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, streaming, blockSize, blocksPerFile, cacheSize)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := stream.StreamOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("stream", &options)

	suite.assert.EqualValues(40, options.StreamCacheSize)
	suite.assert.EqualValues(10, options.BlocksPerFile)
	suite.assert.EqualValues(5, options.BlockSize)
}

func (suite *generateConfigTestSuite) TestCLIParamAttrCache() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	attrCache := "--use-attr-cache"
	cacheOnList := "--cache-on-list=true"
	noSymlinks := "--no-symlinks=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, attrCache, cacheOnList, noSymlinks)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := attr_cache.AttrCacheOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("attr_cache", &options)

	suite.assert.False(options.NoCacheOnList)
	suite.assert.True(options.NoSymlinks)
}

func (suite *generateConfigTestSuite) TestCLIParamStorage() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	adls := "--use-adls=true"
	https := "--use-https=false"
	container := "--container-name=myContainerName"
	concurrency := "--max-concurrency=3"
	cancelListOnMount := "--cancel-list-on-mount-seconds=60"
	maxRetry := "--max-retry=5"
	maxRetryTimeout := "--max-retry-interval-in-seconds=10"
	retryDelayFactor := "--retry-delay-factor=8"
	httpProxy := "--http-proxy=httpProxy"
	httpsProxy := "--https-proxy=httpsProxy"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, adls, https, container, concurrency, cancelListOnMount, maxRetry, maxRetryTimeout, retryDelayFactor, httpProxy, httpsProxy)
	suite.assert.Nil(err)

	// Read the generated v2 config file
	options := azstorage.AzStorageOptions{}

	viper.SetConfigType("yaml")
	config.ReadFromConfigFile(v2ConfigFile.Name())
	config.UnmarshalKey("azstorage", &options)

	suite.assert.EqualValues("adls", options.AccountType)
	suite.assert.True(options.UseHTTP)
	suite.assert.EqualValues("myContainerName", options.Container)
	suite.assert.EqualValues(3, options.MaxConcurrency)
	suite.assert.EqualValues(60, options.CancelListForSeconds)
	suite.assert.EqualValues(5, options.MaxRetries)
	suite.assert.EqualValues(10, options.MaxTimeout)
	suite.assert.EqualValues(8, options.BackoffTime)
	suite.assert.EqualValues("httpProxy", options.HttpProxyAddress)
	suite.assert.EqualValues("httpsProxy", options.HttpsProxyAddress)
}

func (suite *generateConfigTestSuite) TestCLIParamStorageCaCertFileError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	caCertFile := "--ca-cert-file=path"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, caCertFile)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamStorageContentTypeError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	contentType := "--set-content-type=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, contentType)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamStorageBackgroundDownloadError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	download := "--background-download=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, download)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamStorageInvalidateOnSyncNoError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	download := "--invalidate-on-sync=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, download)
	suite.assert.Nil(err)
}

func (suite *generateConfigTestSuite) TestCLIParamPreMountValidateNoError() {
	defer suite.cleanupTest()
	name := generateFileName()
	v2ConfigFile, _ := ioutil.TempFile("", name+".tmp.yaml")
	defer os.Remove(v2ConfigFile.Name())

	outputFile := fmt.Sprintf("--output-file=%s", v2ConfigFile.Name())
	download := "--pre-mount-validate=true"

	_, err := executeCommandC(rootCmd, "mountv1", "--convert-config-only=true", outputFile, download)
	suite.assert.Nil(err)
}
