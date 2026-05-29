// Copyright 2024 The Gitea Authors. All rights reserved.
// Copyright 2024 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"os"
	"testing"

	log "github.com/sirupsen/logrus"
	assert "github.com/stretchr/testify/assert"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

var originalCommonSocketLocations = CommonSocketLocations

func TestGetSocketAndHostWithSocket(t *testing.T) {
	// Arrange
	CommonSocketLocations = originalCommonSocketLocations
	dockerHost := "unix:///my/docker/host.sock"
	socketURI := "/path/to/my.socket"
	t.Setenv("DOCKER_HOST", dockerHost)

	// Act
	ret, err := GetSocketAndHost(socketURI)

	// Assert
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{socketURI, dockerHost}, ret)
}

func TestGetSocketAndHostNoSocket(t *testing.T) {
	// Arrange
	dockerHost := "unix:///my/docker/host.sock"
	t.Setenv("DOCKER_HOST", dockerHost)

	// Act
	ret, err := GetSocketAndHost("")

	// Assert
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{dockerHost, dockerHost}, ret)
}

func TestGetSocketAndHostOnlySocket(t *testing.T) {
	// Arrange
	socketURI := "/path/to/my.socket"
	os.Unsetenv("DOCKER_HOST")
	CommonSocketLocations = originalCommonSocketLocations
	defaultSocket, defaultSocketFound := socketLocation()

	// Act
	ret, err := GetSocketAndHost(socketURI)

	// Assert
	assert.NoError(t, err, "Expected no error from GetSocketAndHost") //nolint:testifylint // pre-existing issue from nektos/act
	assert.True(t, defaultSocketFound, "Expected to find default socket")
	assert.Equal(t, socketURI, ret.Socket, "Expected socket to match common location")
	assert.Equal(t, defaultSocket, ret.Host, "Expected ret.Host to match default socket location")
}

func TestGetSocketAndHostDontMount(t *testing.T) {
	// Arrange
	CommonSocketLocations = originalCommonSocketLocations
	dockerHost := "unix:///my/docker/host.sock"
	t.Setenv("DOCKER_HOST", dockerHost)

	// Act
	ret, err := GetSocketAndHost("-")

	// Assert
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{"-", dockerHost}, ret)
}

func TestGetSocketAndHostNoHostNoSocket(t *testing.T) {
	// Arrange
	CommonSocketLocations = originalCommonSocketLocations
	os.Unsetenv("DOCKER_HOST")
	defaultSocket, found := socketLocation()

	// Act
	ret, err := GetSocketAndHost("")

	// Assert
	assert.True(t, found, "Expected a default socket to be found")
	assert.NoError(t, err, "Expected no error from GetSocketAndHost") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{defaultSocket, defaultSocket}, ret, "Expected to match default socket location")
}

// Catch
// > Your code breaks setting DOCKER_HOST if shouldMount is false.
// > This happens if neither DOCKER_HOST nor --container-daemon-socket has a value, but socketLocation() returns a URI
func TestGetSocketAndHostNoHostNoSocketDefaultLocation(t *testing.T) {
	// Arrange
	mySocketFile, tmpErr := os.CreateTemp(t.TempDir(), "act-*.sock")
	mySocket := mySocketFile.Name()
	unixSocket := "unix://" + mySocket
	defer os.RemoveAll(mySocket)
	assert.NoError(t, tmpErr) //nolint:testifylint // pre-existing issue from nektos/act
	os.Unsetenv("DOCKER_HOST")

	CommonSocketLocations = []string{mySocket}
	defaultSocket, found := socketLocation()

	// Act
	ret, err := GetSocketAndHost("")

	// Assert
	assert.Equal(t, unixSocket, defaultSocket, "Expected default socket to match common socket location")
	assert.True(t, found, "Expected default socket to be found")
	assert.NoError(t, err, "Expected no error from GetSocketAndHost") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{unixSocket, unixSocket}, ret, "Expected to match default socket location")
}

func TestGetSocketAndHostNoHostInvalidSocket(t *testing.T) {
	// Arrange
	os.Unsetenv("DOCKER_HOST")
	mySocket := "/my/socket/path.sock"
	CommonSocketLocations = []string{"/unusual", "/socket", "/location"}
	defaultSocket, found := socketLocation()

	// Act
	ret, err := GetSocketAndHost(mySocket)

	// Assert
	assert.False(t, found, "Expected no default socket to be found")
	assert.Equal(t, "", defaultSocket, "Expected no default socket to be found") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, SocketAndHost{}, ret, "Expected to match default socket location")
	assert.Error(t, err, "Expected an error in invalid state")
}

func TestGetSocketAndHostOnlySocketValidButUnusualLocation(t *testing.T) {
	// Arrange
	socketURI := "unix:///path/to/my.socket"
	CommonSocketLocations = []string{"/unusual", "/location"}
	os.Unsetenv("DOCKER_HOST")
	defaultSocket, found := socketLocation()

	// Act
	ret, err := GetSocketAndHost(socketURI)

	// Assert
	// Default socket locations
	assert.Equal(t, "", defaultSocket, "Expect default socket location to be empty") //nolint:testifylint // pre-existing issue from nektos/act
	assert.False(t, found, "Expected no default socket to be found")
	// Sane default
	assert.NoError(t, err, "Expect no error from GetSocketAndHost") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, socketURI, ret.Host, "Expect host to default to unusual socket")
}
