//go:build windows

package keychain

import (
	"github.com/danieljoos/wincred"
)

func store(target, username, secret string) error {
	cred := wincred.NewGenericCredential(target)
	cred.UserName = username
	cred.CredentialBlob = []byte(secret)
	return cred.Write()
}

func load(target string) (Credential, error) {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		return Credential{}, ErrNotFound
	}
	return Credential{Username: cred.UserName, Secret: string(cred.CredentialBlob)}, nil
}

func delete_(target string) error {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		return ErrNotFound
	}
	return cred.Delete()
}
