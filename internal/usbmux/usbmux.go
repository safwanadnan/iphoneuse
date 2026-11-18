// Package usbmux speaks Apple's usbmuxd protocol: it lists iPhones attached
// over USB (or Wi-Fi sync) and opens TCP connections to ports on them.
package usbmux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"howett.net/plist"
)

const socketPath = "/var/run/usbmuxd"

// Device is one entry from usbmuxd's device list.
type Device struct {
	DeviceID       int
	UDID           string
	ConnectionType string // "USB" or "Network"
}

type header struct {
	Length  uint32
	Version uint32
	Type    uint32
	Tag     uint32
}

func dial() (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("usbmuxd is not reachable: %w", err)
	}
	return conn, nil
}

func send(conn net.Conn, msg map[string]any) error {
	msg["ClientVersionString"] = "iadb"
	msg["ProgName"] = "iadb"
	body, err := plist.Marshal(msg, plist.XMLFormat)
	if err != nil {
		return err
	}
	h := header{Length: uint32(16 + len(body)), Version: 1, Type: 8, Tag: 1}
	if err := binary.Write(conn, binary.LittleEndian, h); err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}

func receive(conn net.Conn, into any) error {
	var h header
	if err := binary.Read(conn, binary.LittleEndian, &h); err != nil {
		return err
	}
	if h.Length < 16 || h.Length > 16<<20 {
		return errors.New("usbmuxd: bad message length")
	}
	body := make([]byte, h.Length-16)
	if _, err := io.ReadFull(conn, body); err != nil {
		return err
	}
	_, err := plist.Unmarshal(body, into)
	return err
}

// List returns the devices usbmuxd currently sees.
func List() ([]Device, error) {
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := send(conn, map[string]any{"MessageType": "ListDevices"}); err != nil {
		return nil, err
	}
	var resp struct {
		DeviceList []struct {
			DeviceID   int
			Properties struct {
				SerialNumber   string
				ConnectionType string
			}
		}
	}
	if err := receive(conn, &resp); err != nil {
		return nil, err
	}
	var devices []Device
	for _, d := range resp.DeviceList {
		devices = append(devices, Device{
			DeviceID:       d.DeviceID,
			UDID:           d.Properties.SerialNumber,
			ConnectionType: d.Properties.ConnectionType,
		})
	}
	return devices, nil
}

// Find returns the usbmuxd entry for a UDID, preferring USB over Wi-Fi.
func Find(udid string) (Device, bool) {
	devices, err := List()
	if err != nil {
		return Device{}, false
	}
	var found Device
	ok := false
	for _, d := range devices {
		if d.UDID != udid {
			continue
		}
		if !ok || d.ConnectionType == "USB" {
			found, ok = d, true
		}
	}
	return found, ok
}

// Dial opens a TCP connection to a port on the device.
func Dial(udid string, port int) (net.Conn, error) {
	dev, ok := Find(udid)
	if !ok {
		return nil, fmt.Errorf("device %s is not connected (plug it in with a cable, and unlock it)", udid)
	}
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	// usbmuxd wants the port in network byte order inside a little-endian int.
	swapped := (port&0xff)<<8 | (port>>8)&0xff
	if err := send(conn, map[string]any{"MessageType": "Connect", "DeviceID": dev.DeviceID, "PortNumber": swapped}); err != nil {
		conn.Close()
		return nil, err
	}
	var resp struct {
		MessageType string
		Number      int
	}
	if err := receive(conn, &resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp.Number != 0 {
		conn.Close()
		return nil, fmt.Errorf("connection to port %d on the device was refused (helper not running)", port)
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}
