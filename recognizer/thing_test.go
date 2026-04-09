/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitTemplate verifies name, mission, service registration, and default trait values.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "YOLOv8" {
		t.Errorf("Name: got %q, want %q", ua.Name, "YOLOv8")
	}
	if ua.Mission != "object_detection" {
		t.Errorf("Mission: got %q, want %q", ua.Mission, "object_detection")
	}
	if _, ok := ua.ServicesMap["recognize"]; !ok {
		t.Error("ServicesMap should contain a 'recognize' service")
	}

	tr, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.YOLOModel == "" {
		t.Error("YOLOModel default should not be empty")
	}
	if tr.PythonCmd == "" {
		t.Error("PythonCmd default should not be empty")
	}
	if tr.DetectScript == "" {
		t.Error("DetectScript default should not be empty")
	}
}

// TestServing_Recognize_MethodNotAllowed verifies that non-GET requests to recognize return 405.
func TestServing_Recognize_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/recognizer/YOLOv8/recognize", nil)
	serving(tr, w, r, "recognize")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestServing_InvalidService verifies that unknown service paths return 400.
func TestServing_InvalidService(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/recognizer/YOLOv8/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestDownloadFile_Success verifies that downloadFile writes the server's response body
// to a temporary file and returns a valid path.
func TestDownloadFile_Success(t *testing.T) {
	content := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(content)
	}))
	defer srv.Close()

	path, err := downloadFile(srv.URL)
	if err != nil {
		t.Fatalf("downloadFile returned error: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("file content mismatch: got %v, want %v", got, content)
	}
}

// TestDownloadFile_Unreachable verifies that an unreachable URL returns an error.
func TestDownloadFile_Unreachable(t *testing.T) {
	_, err := downloadFile("http://127.0.0.1:1/no-such-server")
	if err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
}

// TestRunYOLO_Success verifies that runYOLO returns the labels printed by the detection script.
func TestRunYOLO_Success(t *testing.T) {
	// Write a stub script that creates the output file and prints two labels.
	script := writeTempScript(t, `
import sys, os
out = sys.argv[2]
os.makedirs(os.path.dirname(out) if os.path.dirname(out) else '.', exist_ok=True)
open(out, 'wb').write(b'\xff\xd8')  # minimal JPEG header
print('person')
print('chair')
`)
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "annotated.jpg")

	tr := &Traits{PythonCmd: "python3", DetectScript: script, YOLOModel: "yolov8n.pt"}
	labels, err := tr.runYOLO("input.jpg", outPath)
	if err != nil {
		t.Fatalf("runYOLO returned error: %v", err)
	}
	if len(labels) != 2 || labels[0] != "person" || labels[1] != "chair" {
		t.Errorf("labels: got %v, want [person chair]", labels)
	}
}

// TestRunYOLO_NoDetections verifies that an empty stdout produces an empty label slice.
func TestRunYOLO_NoDetections(t *testing.T) {
	script := writeTempScript(t, `
import sys, os
out = sys.argv[2]
os.makedirs(os.path.dirname(out) if os.path.dirname(out) else '.', exist_ok=True)
open(out, 'wb').write(b'\xff\xd8')
# no output — nothing detected
`)
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "annotated.jpg")

	tr := &Traits{PythonCmd: "python3", DetectScript: script, YOLOModel: "yolov8n.pt"}
	labels, err := tr.runYOLO("input.jpg", outPath)
	if err != nil {
		t.Fatalf("runYOLO returned error: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("expected no labels, got %v", labels)
	}
}

// TestRunYOLO_ScriptFails verifies that a non-zero exit from the detection script returns an error.
func TestRunYOLO_ScriptFails(t *testing.T) {
	script := writeTempScript(t, `import sys; sys.exit(1)`)

	tr := &Traits{PythonCmd: "python3", DetectScript: script, YOLOModel: "yolov8n.pt"}
	_, err := tr.runYOLO("input.jpg", filepath.Join(t.TempDir(), "out.jpg"))
	if err == nil {
		t.Error("expected error when script exits with code 1, got nil")
	}
}

// TestRunYOLO_LabelWhitespaceTrimming verifies that blank lines in script output are ignored.
func TestRunYOLO_LabelWhitespaceTrimming(t *testing.T) {
	script := writeTempScript(t, `
import sys, os
out = sys.argv[2]
os.makedirs(os.path.dirname(out) if os.path.dirname(out) else '.', exist_ok=True)
open(out, 'wb').write(b'\xff\xd8')
print('\nperson\n\nlaptop\n')
`)
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "annotated.jpg")

	tr := &Traits{PythonCmd: "python3", DetectScript: script, YOLOModel: "yolov8n.pt"}
	labels, err := tr.runYOLO("input.jpg", outPath)
	if err != nil {
		t.Fatalf("runYOLO returned error: %v", err)
	}
	for _, l := range labels {
		if strings.TrimSpace(l) == "" {
			t.Errorf("blank label found in output: %v", labels)
		}
	}
	if len(labels) != 2 {
		t.Errorf("expected 2 labels, got %v", labels)
	}
}

// TestTraitsJSON verifies that Traits fields round-trip correctly through JSON unmarshalling,
// matching the structure used in systemconfig.json.
func TestTraitsJSON(t *testing.T) {
	raw := []byte(`{
		"functionalLocation": "Entrance",
		"yoloModel":          "yolov8s.pt",
		"pythonCmd":          "/usr/bin/python3",
		"detectScript":       "/opt/recognizer/detect.py"
	}`)

	var tr Traits
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if tr.FunctionalLocation != "Entrance" {
		t.Errorf("FunctionalLocation: got %q", tr.FunctionalLocation)
	}
	if tr.YOLOModel != "yolov8s.pt" {
		t.Errorf("YOLOModel: got %q", tr.YOLOModel)
	}
	if tr.PythonCmd != "/usr/bin/python3" {
		t.Errorf("PythonCmd: got %q", tr.PythonCmd)
	}
	if tr.DetectScript != "/opt/recognizer/detect.py" {
		t.Errorf("DetectScript: got %q", tr.DetectScript)
	}
}

// ------------------------------------- helpers

// writeTempScript writes a Python script to a temporary file and returns its path.
func writeTempScript(t *testing.T, code string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stub-*.py")
	if err != nil {
		t.Fatalf("creating temp script: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(code); err != nil {
		t.Fatalf("writing temp script: %v", err)
	}
	return f.Name()
}
