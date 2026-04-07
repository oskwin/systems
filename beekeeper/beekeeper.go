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
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("beekeeper", ctx)

	sys.Husk = &components.Husk{
		Description: "exposes ZigBee devices paired to a RaspBee II / deCONZ gateway as Arrowhead services",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20185, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/beekeeper",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
		RegistrarChan: make(chan *components.CoreSystem, 1),
		Messengers:    make(map[string]int),
	}

	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset)

	if len(rawResources) == 0 {
		log.Fatal("beekeeper: no unit asset configuration found in systemconfig.json")
	}

	var uac usecases.ConfigurableAsset
	if err := json.Unmarshal(rawResources[0], &uac); err != nil {
		log.Fatalf("resource configuration error: %v\n", err)
	}
	assets, cleanup := newResources(uac, &sys)
	defer cleanup()
	for _, ua := range assets {
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving handles incoming HTTP requests for a ZigBee device unit asset.
// All services are read-only (GET); the value is looked up from the shared cache.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
		return
	}

	m := t.cache.get(t.assetName, servicePath)
	if m == nil {
		http.Error(w, "measurement not yet available", http.StatusServiceUnavailable)
		return
	}

	if m.IsBool {
		var f forms.SignalB_v1a
		f.NewForm()
		f.Value = m.BoolValue
		f.Timestamp = m.Timestamp
		usecases.HTTPProcessGetRequest(w, r, &f)
	} else {
		var f forms.SignalA_v1a
		f.NewForm()
		f.Value = m.Value
		f.Unit = serviceUnit(servicePath)
		f.Timestamp = m.Timestamp
		usecases.HTTPProcessGetRequest(w, r, &f)
	}
}

// serviceUnit returns the physical unit for a given service subpath.
func serviceUnit(s string) string {
	if spec, ok := serviceSpecs[s]; ok {
		return spec.unit
	}
	return ""
}
