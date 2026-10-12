package main

import "os"

// Export all embedded sizes, preserving the same transparent icon used by the
// executable, native windows, notification area, and HTML application branding.
func writeApplicationIcon(file string) error {
	data, err := uiFiles.ReadFile("ui/app-icon.ico")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
}
