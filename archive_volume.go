package main

import "regexp"

var rarVolumePattern = regexp.MustCompile(`(?i)(\.rar|\.[r-z][0-9]{2})$`)

func isRARVolume(name string) bool { return rarVolumePattern.MatchString(name) }
