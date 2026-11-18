// Package signing finds the Apple developer teams this Mac can sign with and
// reads provisioning profile expiry dates.
package signing

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"howett.net/plist"
)

// Team is a signing team (a free "Personal Team" or a paid membership).
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Free bool   `json:"free"`
}

// Teams merges the teams Xcode knows about with Apple Development
// certificates found in the login keychain.
func Teams() []Team {
	byID := map[string]Team{}
	for _, t := range xcodeTeams() {
		byID[t.ID] = t
	}
	for _, t := range keychainTeams() {
		if _, ok := byID[t.ID]; !ok {
			byID[t.ID] = t
		}
	}
	var teams []Team
	for _, t := range byID {
		teams = append(teams, t)
	}
	// Paid teams first: their profiles last a year instead of 7 days.
	sort.Slice(teams, func(i, j int) bool {
		if teams[i].Free != teams[j].Free {
			return !teams[i].Free
		}
		return teams[i].Name < teams[j].Name
	})
	return teams
}

func xcodeTeams() []Team {
	out, err := exec.Command("/usr/bin/defaults", "export", "com.apple.dt.Xcode", "-").Output()
	if err != nil {
		return nil
	}
	var prefs map[string]any
	if _, err := plist.Unmarshal(out, &prefs); err != nil {
		return nil
	}
	var teams []Team
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["teamID"].(string); ok && id != "" {
				name, _ := t["teamName"].(string)
				free, _ := t["isFreeProvisioningTeam"].(bool)
				teams = append(teams, Team{ID: id, Name: name, Free: free})
				return
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	for _, key := range []string{"IDEProvisioningTeamByIdentifier", "IDEProvisioningTeams"} {
		walk(prefs[key])
	}
	return teams
}

func keychainTeams() []Team {
	out, err := exec.Command("/usr/bin/security", "find-certificate", "-a", "-p", "-c", "Apple Development").Output()
	if err != nil {
		return nil
	}
	var teams []Team
	rest := out
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || time.Now().After(cert.NotAfter) || len(cert.Subject.OrganizationalUnit) == 0 {
			continue
		}
		name := ""
		if len(cert.Subject.Organization) > 0 {
			name = cert.Subject.Organization[0]
		}
		// Keychain certs don't say whether the team is free; Xcode prefs do.
		teams = append(teams, Team{ID: cert.Subject.OrganizationalUnit[0], Name: name, Free: true})
	}
	return teams
}

// ProfileExpiry reads ExpirationDate from an app's embedded.mobileprovision.
func ProfileExpiry(path string) (time.Time, error) {
	_, exp, err := ProfileInfo(path)
	return exp, err
}

// ProfileInfo reads a provisioning profile's name and expiry date.
func ProfileInfo(path string) (string, time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, err
	}
	// The profile is a CMS envelope around an XML plist; pull the plist out.
	start := bytes.Index(data, []byte("<?xml"))
	end := bytes.Index(data, []byte("</plist>"))
	if start < 0 || end < 0 {
		return "", time.Time{}, errors.New("not a provisioning profile")
	}
	var profile struct {
		Name           string
		ExpirationDate time.Time
	}
	if _, err := plist.Unmarshal(data[start:end+len("</plist>")], &profile); err != nil {
		return "", time.Time{}, err
	}
	return profile.Name, profile.ExpirationDate, nil
}

// ProfileDirs are where Xcode caches provisioning profiles.
func ProfileDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, "Library", "MobileDevice", "Provisioning Profiles"),
		filepath.Join(home, "Library", "Developer", "Xcode", "UserData", "Provisioning Profiles"),
	}
}
