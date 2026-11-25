package core

import (
	"context"
	"fmt"
)

// certificateKeys are the keys a certificate creation writes in a sec.
var certificateKeys = []string{"private_key", "certificate", "certificate_chain", "fullpem", "serial_number"}

type ObjectCertificateCreation struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	// Keys are the certificate keys of the sec, read again: names and sizes,
	// never their values.
	Keys      []ObjectDataKey `json:"keys" jsonschema:"the certificate keys of the sec read again, private_key, certificate, certificate_chain, fullpem and serial_number, by name and stored size; their values are never returned"`
	KeysError string          `json:"keys_error,omitempty" jsonschema:"why the keys could not be read again; the certificate is created"`
}

type ObjectCertificateRenewal struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Renewed    bool                   `json:"renewed" jsonschema:"whether a certificate was issued"`
	Reason     string                 `json:"reason,omitempty" jsonschema:"why a certificate was issued, or why none was, such as one not due; untrusted text"`
	Domains    []string               `json:"domains,omitempty" jsonschema:"the domains of the certificate"`
	Directory  string                 `json:"directory,omitempty" jsonschema:"the ACME directory the certificate is issued by, empty for a certificate generated"`
	NotAfter   string                 `json:"not_after,omitempty" jsonschema:"the end of validity of the certificate"`
}

type daemonCertificateRenewal struct {
	Renewed   bool     `json:"renewed"`
	Reason    string   `json:"reason"`
	Domains   []string `json:"domains"`
	Directory string   `json:"directory"`
	NotAfter  string   `json:"not_after"`
}

// CreateObjectCertificate creates the certificate of a sec: the daemon keeps
// its private key, or generates one, and writes the certificate, signed by
// the sec its ca keyword names, or self-signed. It replaces a certificate
// the sec holds.
func (s *Service) CreateObjectCertificate(ctx context.Context, path string) (ObjectCertificateCreation, error) {
	reference, err := validateCertificateTarget(path)
	if err != nil {
		return ObjectCertificateCreation{}, err
	}
	poster, ok := s.client.(JSONPoster)
	if !ok {
		return ObjectCertificateCreation{}, fmt.Errorf("OpenSVC daemon client does not support POST requests")
	}
	endpoint := fmt.Sprintf("/api/object/path/%s/sec/%s/certificate/create", reference.Namespace, reference.Name)
	// The daemon answers 204.
	if err := poster.PostJSON(ctx, endpoint, nil, nil, nil); err != nil {
		return ObjectCertificateCreation{}, fmt.Errorf("create certificate of %s: %w", reference.Path, err)
	}
	result := ObjectCertificateCreation{Object: reference, Keys: []ObjectDataKey{}}
	keys, err := s.ListObjectDataKeys(ctx, ListObjectDataKeysOptions{Path: reference.Path})
	if err != nil {
		result.KeysError = err.Error()
	} else {
		for _, key := range keys.Keys {
			for _, name := range certificateKeys {
				if key.Name == name {
					result.Keys = append(result.Keys, key)
				}
			}
		}
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// RenewObjectCertificate renews the certificate of a sec when it is due:
// from its ACME directory, or generated again. The daemon answers renewed
// false, with the reason, for one not due, and refuses with 409 a
// certificate it did not issue.
func (s *Service) RenewObjectCertificate(ctx context.Context, path string) (ObjectCertificateRenewal, error) {
	reference, err := validateCertificateTarget(path)
	if err != nil {
		return ObjectCertificateRenewal{}, err
	}
	poster, ok := s.client.(JSONPoster)
	if !ok {
		return ObjectCertificateRenewal{}, fmt.Errorf("OpenSVC daemon client does not support POST requests")
	}
	endpoint := fmt.Sprintf("/api/object/path/%s/sec/%s/certificate/renew", reference.Namespace, reference.Name)
	var renewal daemonCertificateRenewal
	if err := poster.PostJSON(ctx, endpoint, nil, nil, &renewal); err != nil {
		return ObjectCertificateRenewal{}, fmt.Errorf("renew certificate of %s: %w", reference.Path, err)
	}
	reason, _ := cutUTF8(renewal.Reason, 2000)
	return ObjectCertificateRenewal{
		Provenance: s.newProvenance(), Object: reference, Renewed: renewal.Renewed, Reason: reason,
		Domains: renewal.Domains, Directory: renewal.Directory, NotAfter: renewal.NotAfter,
	}, nil
}

func validateCertificateTarget(path string) (ClusterObjectReference, error) {
	if hasSelectorCharacters(path) {
		return ClusterObjectReference{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	reference, err := validateExactObjectPath(path)
	if err != nil {
		return ClusterObjectReference{}, err
	}
	if reference.Kind != "sec" {
		return ClusterObjectReference{}, fmt.Errorf("a certificate is held by a sec object only")
	}
	return reference, nil
}
