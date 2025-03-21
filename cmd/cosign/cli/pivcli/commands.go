// +build !pivkeydisabled
// +build cgo

// Copyright 2021 The Sigstore Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pivcli

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-piv/piv-go/piv"
	"github.com/manifoldco/promptui"
	"github.com/sigstore/cosign/pkg/cosign/pivkey"
)

func SetManagementKeyCmd(_ context.Context, oldKey, newKey string, randomKey bool) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()

	oldBytes, err := keyBytes(oldKey)
	if err != nil {
		return err
	}
	var newBytes *[24]byte
	if randomKey {
		if !Confirm("Resetting management key to random value. You must factory reset the device to change this value") {
			return nil
		}
		newBytes, err = randomManagementKey()
		if err != nil {
			return err
		}
	} else {
		newBytes, err = keyBytes(newKey)
		if err != nil {
			return err
		}
	}
	if !Confirm("Setting new management key. This will overwrite the previous key.") {
		return nil
	}
	return yk.SetManagementKey(*oldBytes, *newBytes)
}

func SetPukCmd(_ context.Context, oldPuk, newPuk string) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()
	if oldPuk == "" {
		oldPuk = piv.DefaultPUK
	}
	if newPuk == "" {
		newPuk = piv.DefaultPUK
	}
	if !Confirm("Setting new PUK. This will overwrite the previous PUK.") {
		return nil
	}
	return yk.SetPUK(oldPuk, newPuk)
}

func UnblockCmd(_ context.Context, oldPuk, newPin string) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()
	if oldPuk == "" {
		oldPuk = piv.DefaultPUK
	}
	if newPin == "" {
		newPin = piv.DefaultPIN
	}
	if !Confirm("Unblocking the device. This will set a new pin.") {
		return nil
	}
	return yk.Unblock(oldPuk, newPin)
}

func SetPinCmd(_ context.Context, oldPin, newPin string) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()

	if oldPin == "" {
		oldPin = piv.DefaultPIN
	}
	if newPin == "" {
		newPin = piv.DefaultPIN
	}
	if !Confirm("Setting new pin. This will overwrite the previous pin.") {
		return nil
	}
	return yk.SetPIN(oldPin, newPin)
}

type Attestations struct {
	DeviceCert     *x509.Certificate
	KeyCert        *x509.Certificate
	KeyAttestation *piv.Attestation
}

func AttestationCmd(_ context.Context) (*Attestations, error) {
	yk, err := pivkey.GetKey()
	if err != nil {
		return nil, err
	}
	defer yk.Close()
	deviceCert, err := yk.AttestationCertificate()
	if err != nil {
		return nil, err
	}

	fmt.Fprintln(os.Stderr, "Printing device attestation certificate")
	b := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: deviceCert.Raw,
	})
	fmt.Println(string(b))

	keyCert, err := yk.Attest(piv.SlotSignature)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "Printing key attestation certificate")
	b = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: keyCert.Raw,
	})
	fmt.Println(string(b))

	fmt.Println("Verifying certificates...")
	a, err := piv.Verify(deviceCert, keyCert)
	if err != nil {
		return nil, err
	}
	fmt.Println("Verified ok")
	fmt.Println()

	fmt.Fprintln(os.Stderr, "Device info:")
	fmt.Println("  Issuer:", deviceCert.Issuer)
	fmt.Println("  Form factor:", formFactorString(a.Formfactor))
	fmt.Println("  PIN Policy:", pinPolicyStr(a.PINPolicy))

	fmt.Printf("  Serial number: %d\n", a.Serial)
	fmt.Printf("  Version: %d.%d.%d\n", a.Version.Major, a.Version.Minor, a.Version.Patch)

	ret := &Attestations{
		DeviceCert:     deviceCert,
		KeyCert:        keyCert,
		KeyAttestation: a,
	}

	return ret, nil
}

func GenerateKeyCmd(ctx context.Context, managementKey string, randomKey bool) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()
	keyBytes, err := keyBytes(managementKey)
	if err != nil {
		return err
	}

	if randomKey {
		if !Confirm("Resetting management key to random value. You must factory reset the device to change this value") {
			return nil
		}
		newKeyBytes, err := randomManagementKey()
		if err != nil {
			return err
		}
		if err := yk.SetManagementKey(*keyBytes, *newKeyBytes); err != nil {
			return err
		}
		keyBytes = newKeyBytes
	}

	key := piv.Key{
		Algorithm:   piv.AlgorithmEC256,
		PINPolicy:   piv.PINPolicyAlways,
		TouchPolicy: piv.TouchPolicyAlways,
	}
	if !Confirm("Generating new signing key. This will destroy any previous keys.") {
		return nil
	}
	pubKey, err := yk.GenerateKey(*keyBytes, piv.SlotSignature, key)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Generated public key")
	b, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: b,
	})

	fmt.Println(string(pemBytes))
	yk.Close()
	_, err = AttestationCmd(ctx)
	return err
}

func ResetKeyCmd(ctx context.Context) error {
	yk, err := pivkey.GetKey()
	if err != nil {
		return err
	}
	defer yk.Close()
	if !Confirm("Resetting key to factory defaults. This will destroy all values on device.") {
		return nil
	}

	return yk.Reset()
}

func keyBytes(s string) (*[24]byte, error) {
	if s == "" {
		return &piv.DefaultManagementKey, nil
	}
	if len(s) > 24 {
		return nil, errors.New("key too long, must be <24 characters")
	}
	ret := [24]byte{}
	copy(ret[:], s)
	return &ret, nil
}

var Confirm = func(p string) bool {
	prompt := promptui.Prompt{
		Label:     p,
		IsConfirm: true,
	}

	result, err := prompt.Run()
	if err != nil {
		fmt.Println(err)
		return false
	}
	return strings.ToLower(result) == "y"
}

func randomManagementKey() (*[24]byte, error) {
	var newKeyBytes [24]byte
	n, err := io.ReadFull(rand.Reader, newKeyBytes[:])
	if err != nil {
		return nil, err
	}
	if n != len(newKeyBytes) {
		return nil, errors.New("short read from random")
	}
	return &newKeyBytes, nil
}

func formFactorString(ff piv.Formfactor) string {
	switch ff {
	case piv.FormfactorUSBAKeychain:
		return "USB A Keychain"
	case piv.FormfactorUSBANano:
		return "USB A Nano"
	case piv.FormfactorUSBCKeychain:
		return "USB C Keychain"
	case piv.FormfactorUSBCNano:
		return "USB C Nano"
	case piv.FormfactorUSBCLightningKeychain:
		return "USB C Lighting Keychain"
	default:
		return fmt.Sprintf("unknown: %d", ff)
	}
}

func pinPolicyStr(pp piv.PINPolicy) string {
	switch pp {
	case piv.PINPolicyAlways:
		return "Always"
	case piv.PINPolicyNever:
		return "Never"
	case piv.PINPolicyOnce:
		return "Once"
	default:
		return "unknown"
	}
}
