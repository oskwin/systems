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
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------- Define the unit asset

// Traits holds the configurable parameters for the YOLO unit asset.
type Traits struct {
	FunctionalLocation string `json:"functionalLocation"` // optional: filter photograph service by location
	YOLOModel          string `json:"yoloModel"`          // YOLO model file (default: yolov8n.pt)
	PythonCmd          string `json:"pythonCmd"`          // Python interpreter (default: python3)
	DetectScript       string `json:"detectScript"`       // path to the detection script (default: detect.py)

	owner *components.System
	ua    *components.UnitAsset
}

// ------------------------------------- Instantiate a unit asset template

// initTemplate returns a template UnitAsset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	recognizeSvc := components.Service{
		Definition:  "recognize",
		SubPath:     "recognize",
		Details:     map[string][]string{"Forms": {"FileForm_v1"}},
		RegPeriod:   30,
		Description: "triggers a photograph, runs YOLOv8 detection, and returns the annotated image URL (GET)",
	}

	return &components.UnitAsset{
		Name:    "YOLOv8",
		Mission: "object_detection",
		Details: map[string][]string{"Model": {"Ultralytics_YOLOv8"}},
		ServicesMap: components.Services{
			recognizeSvc.SubPath: &recognizeSvc,
		},
		Traits: &Traits{
			YOLOModel:    "yolov8n.pt",
			PythonCmd:    "python3",
			DetectScript: "detect.py",
		},
	}
}

// ------------------------------------- Instantiate a unit asset based on configuration

// newResource creates the runtime unit asset from the configuration file.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		YOLOModel:    "yolov8n.pt",
		PythonCmd:    "python3",
		DetectScript: "detect.py",
		owner:        sys,
	}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("recognizer: could not unmarshal traits:", err)
		}
	}

	// Cervice for the photograph service, optionally filtered by FunctionalLocation.
	photographDetails := map[string][]string{}
	if t.FunctionalLocation != "" {
		photographDetails["FunctionalLocation"] = []string{t.FunctionalLocation}
	}
	photographCer := &components.Cervice{
		Definition: "photograph",
		Protos:     components.SProtocols(sys.Husk.ProtoPort),
		Details:    photographDetails,
		Nodes:      make(map[string][]components.NodeInfo),
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: components.Cervices{"photograph": photographCer},
		Traits:      t,
	}
	t.ua = ua
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua, func() {}
}

// ------------------------------------- Pipeline

// runPipeline orchestrates the full recognition cycle:
//  1. GET the photograph service → FileForm_v1 with the source JPEG URL
//  2. Download the JPEG
//  3. Run YOLOv8 detection → annotated JPEG saved to files/
//  4. Return a FileForm_v1 pointing to the annotated image
func (t *Traits) runPipeline() (*forms.FileForm_v1, error) {
	// Step 1: get a fresh photograph via the Arrowhead orchestrator.
	cer := t.ua.CervicesMap["photograph"]
	photoForm, err := usecases.GetState(cer, t.owner)
	if err != nil {
		return nil, fmt.Errorf("getting photograph: %w", err)
	}
	ff, ok := photoForm.(*forms.FileForm_v1)
	if !ok {
		return nil, fmt.Errorf("unexpected form type from photograph service: %T", photoForm)
	}
	log.Printf("recognizer: source image: %s\n", ff.FileURL)

	// Step 2: download the JPEG.
	srcPath, err := downloadFile(ff.FileURL)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer os.Remove(srcPath) // clean up temp file

	// Step 3: run YOLO detection.
	timestamp := time.Now().Format("20060102-150405")
	outFilename := fmt.Sprintf("annotated_%s.jpg", timestamp)
	outPath := filepath.Join("files", outFilename)

	labels, err := t.runYOLO(srcPath, outPath)
	if err != nil {
		return nil, fmt.Errorf("YOLO detection: %w", err)
	}

	if len(labels) > 0 {
		log.Printf("recognizer: detected objects: %s\n", strings.Join(labels, ", "))
	} else {
		log.Println("recognizer: no objects detected")
	}

	// Step 4: build the response form.
	host := t.owner.Husk.Host.IPAddresses[0]
	port := t.owner.Husk.ProtoPort["http"]
	annotatedURL := fmt.Sprintf("http://%s:%d/recognizer/%s/files/%s", host, port, t.ua.Name, outFilename)

	var result forms.FileForm_v1
	result.NewForm()
	result.FileURL = annotatedURL
	result.Timestamp = time.Now()
	return &result, nil
}

// downloadFile fetches a URL and writes it to a temporary file, returning the path.
func downloadFile(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp("", "recognizer-*.jpg")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// runYOLO calls detect.py with the source image path and output path.
// It returns the list of detected object labels printed by the script.
func (t *Traits) runYOLO(srcPath, outPath string) ([]string, error) {
	if err := os.MkdirAll("files", os.ModePerm); err != nil {
		return nil, fmt.Errorf("creating files directory: %w", err)
	}

	cmd := exec.Command(t.PythonCmd, t.DetectScript, srcPath, outPath, t.YOLOModel)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("detect.py: %w", err)
	}

	// detect.py prints one label per line on stdout.
	var labels []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" {
			labels = append(labels, line)
		}
	}
	return labels, nil
}
