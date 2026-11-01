//go:build windows

/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package shared

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protected state on windows. The unix implementation walks descriptor-relative
// with O_NOFOLLOW and checks mode bits and uid. windows has neither: the equivalent
// is to refuse reparse points (symbolic links and junctions), to read the facts from
// the OPEN HANDLE (not from the path), and to decide with checkProtectedACL that the
// owner is the process's user, the administrators or the system and that nobody else
// is granted write (and, for a file, read) access.
//
// Residual window, documented: each path segment is opened by path, not relative to
// its parent's handle, so an attacker who can already replace a segment of a
// directory that this code has just verified as writable by trusted principals only
// has nothing left to gain.

const (
	fileShareAll        = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	directoryAccess     = windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
	lockOffsetHigh      = 0x40000000 // the lock lives far beyond the content, so readers are never blocked
	errorLockViolation  = windows.Errno(33)
	errorSharingViolate = windows.Errno(32)
)

func currentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func trustedFor(allowed []string) (map[string]struct{}, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, errors.New("current user unavailable")
	}
	return trustedSIDs(user, allowed), nil
}

// openNoFollow opens an object without following a final reparse point.
func openNoFollow(name string, access, disposition uint32, directory bool) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	} else {
		flags |= windows.FILE_ATTRIBUTE_NORMAL
	}
	return windows.CreateFile(pointer, access, fileShareAll, nil, disposition, flags, 0)
}

// factsOf reads the owner and the allow entries of an open handle.
func factsOf(handle windows.Handle) (aclFacts, error) {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return aclFacts{}, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return aclFacts{}, errUnsafeACL
	}
	facts := aclFacts{Owner: owner.String()}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return aclFacts{}, err
	}
	if dacl == nil {
		return facts, nil // HasDACL stays false: everyone has access
	}
	facts.HasDACL = true
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return aclFacts{}, err
		}
		entry := aclEntry{
			SID:         (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(),
			Mask:        uint32(ace.Mask),
			InheritOnly: ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0,
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, 9: // 9: allowed with a condition, same leading layout
			entry.Allow = true
		case windows.ACCESS_DENIED_ACE_TYPE, 10:
			entry.Allow = false
		default:
			// An object-specific or audit entry has another layout: do not guess.
			return aclFacts{}, errUnsafeACL
		}
		facts.Entries = append(facts.Entries, entry)
	}
	return facts, nil
}

// inspect validates an open handle and returns its file information.
func inspect(handle windows.Handle, directory bool, trusted map[string]struct{}) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return info, errors.New("protected object metadata unavailable")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return info, errors.New("protected object is a link")
	}
	if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return info, errors.New("protected object has the wrong type")
	}
	facts, err := factsOf(handle)
	if err != nil {
		return info, errors.New("protected object access list unavailable")
	}
	if err := checkProtectedACL(facts, trusted, directory); err != nil {
		if directory {
			return info, errors.New("unsafe protected directory")
		}
		return info, errors.New("unsafe protected file ownership or permissions")
	}
	return info, nil
}

func checkDirectory(name string, trusted map[string]struct{}) error {
	handle, err := openNoFollow(name, directoryAccess, windows.OPEN_EXISTING, true)
	if err != nil {
		return errors.New("protected directory unavailable")
	}
	defer windows.CloseHandle(handle)
	_, err = inspect(handle, true, trusted)
	return err
}

// ValidateProtectedDirectory verifies the root used by protected security state.
// allowedOwnerUIDs is a unix notion and is ignored here.
func ValidateProtectedDirectory(root string, _ []uint32) error {
	if root == "" {
		return errors.New("invalid protected directory configuration")
	}
	trusted, err := trustedFor(nil)
	if err != nil {
		return err
	}
	return checkDirectory(root, trusted)
}

// walkProtected validates the root and each directory segment of relative, creating
// missing ones when create is set, and returns the directory that holds the file.
func walkProtected(root string, segments []string, trusted map[string]struct{}, create bool) (string, error) {
	if err := checkDirectory(root, trusted); err != nil {
		return "", err
	}
	current := root
	for _, segment := range segments {
		next := filepath.Join(current, segment)
		if _, err := os.Lstat(next); os.IsNotExist(err) && create {
			if mkErr := os.Mkdir(next, 0o700); mkErr != nil && !os.IsExist(mkErr) {
				return "", errors.New("protected directory creation failed")
			}
		}
		if err := checkDirectory(next, trusted); err != nil {
			return "", errors.New("protected path unavailable")
		}
		current = next
	}
	return current, nil
}

// WriteProtectedFile atomically replaces one owner-only file beneath a protected root.
func WriteProtectedFile(root, relative string, content []byte, mode os.FileMode) error {
	if root == "" || !ValidProtectedRelativePath(relative) || mode.Perm()&0o077 != 0 {
		return errors.New("invalid protected file configuration")
	}
	trusted, err := trustedFor(nil)
	if err != nil {
		return err
	}
	segments := strings.Split(relative, "/")
	directory, err := walkProtected(root, segments[:len(segments)-1], trusted, true)
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("protected temporary name unavailable")
	}
	temporary := filepath.Join(directory, ".tmp-"+hex.EncodeToString(random[:]))
	handle, err := openNoFollow(temporary, windows.GENERIC_WRITE, windows.CREATE_NEW, false)
	if err != nil {
		return errors.New("protected temporary file unavailable")
	}
	opened := os.NewFile(uintptr(handle), "protected temporary file")
	cleanup := func() {
		_ = opened.Close()
		_ = os.Remove(temporary)
	}
	if _, err := opened.Write(content); err != nil {
		cleanup()
		return errors.New("protected file write failed")
	}
	if err := opened.Sync(); err != nil {
		cleanup()
		return errors.New("protected file sync failed")
	}
	if err := opened.Close(); err != nil {
		_ = os.Remove(temporary)
		return errors.New("protected file close failed")
	}
	target := filepath.Join(directory, segments[len(segments)-1])
	// A read-only target cannot be replaced; the directory is protected, so lifting
	// the attribute for the replacement does not widen access.
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode().Perm()&0o200 == 0 {
		_ = os.Chmod(target, 0o600)
	}
	if err := AtomicReplace(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return errors.New("protected file publication failed")
	}
	if mode.Perm()&0o200 == 0 {
		_ = os.Chmod(target, mode.Perm())
	}
	return nil
}

// ReadProtectedFile reads one regular file beneath a protected directory. Returned
// errors contain no path.
func ReadProtectedFile(root, relative string, options ProtectedFileOptions) ([]byte, error) {
	if root == "" || !ValidProtectedRelativePath(relative) || options.MaxBytes < 1 {
		return nil, errors.New("invalid protected file configuration")
	}
	trusted, err := trustedFor(options.AllowedOwnerSIDs)
	if err != nil {
		return nil, err
	}
	segments := strings.Split(relative, "/")
	directory, err := walkProtected(root, segments[:len(segments)-1], trusted, false)
	if err != nil {
		return nil, err
	}
	handle, err := openNoFollow(filepath.Join(directory, segments[len(segments)-1]), windows.GENERIC_READ, windows.OPEN_EXISTING, false)
	if err != nil {
		return nil, errors.New("protected file unavailable")
	}
	opened := os.NewFile(uintptr(handle), "protected file")
	defer opened.Close()

	info, err := inspect(handle, false, trusted)
	if err != nil {
		return nil, err
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	if info.NumberOfLinks != 1 || size <= 0 || size > options.MaxBytes {
		return nil, errors.New("invalid protected file metadata")
	}
	if options.RequireOwnerReadOnly && info.FileAttributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return nil, errors.New("unsafe protected file ownership or permissions")
	}
	content, err := io.ReadAll(io.LimitReader(opened, options.MaxBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > options.MaxBytes || int64(len(content)) != size {
		ZeroBytes(content)
		return nil, errors.New("invalid protected file content size")
	}
	return content, nil
}

// OpenProtectedFile opens one regular file read-write, creating it when create is
// set, and refuses a link, a hard-linked file, or one whose owner or access list is
// not trusted. The directory that holds it must already have been validated.
func OpenProtectedFile(filePath string, create bool, _ []uint32, allowedOwnerSIDs []string) (*os.File, error) {
	trusted, err := trustedFor(allowedOwnerSIDs)
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	handle, err := openNoFollow(filePath, windows.GENERIC_READ|windows.GENERIC_WRITE, disposition, false)
	if err != nil {
		return nil, errors.New("protected file unavailable")
	}
	file := os.NewFile(uintptr(handle), "protected file")
	info, err := inspect(handle, false, trusted)
	if err != nil || info.NumberOfLinks != 1 {
		_ = file.Close()
		return nil, errors.New("unsafe protected file")
	}
	return file, nil
}

// LockFile takes an exclusive lock on the file. The locked range lies far beyond the
// content so that other processes can still read the file, as with an advisory lock.
// With wait false it returns (false, nil) when another holder has it.
func LockFile(file *os.File, wait bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	overlapped := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlapped); err != nil {
		if !wait && (err == errorLockViolation || err == errorSharingViolate || err == windows.ERROR_IO_PENDING) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// UnlockFile releases a lock taken by LockFile.
func UnlockFile(file *os.File) error {
	overlapped := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}

// CreateProtectedDirectory creates the directory (and parents) for protected state.
// A directory this call creates gets an explicit, non-inherited access list that
// grants full control to the current user, the administrators and the system only,
// inheritable by what is created inside. An existing directory is left untouched:
// ValidateProtectedDirectory decides whether it is acceptable.
func CreateProtectedDirectory(directory string) error {
	if _, err := os.Lstat(directory); err == nil {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	var entries []windows.EXPLICIT_ACCESS
	for _, sidText := range []string{user, sidSystem, sidAdministrators} {
		sid, err := windows.StringToSid(sidText)
		if err != nil {
			return err
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// InspectProtectedPath validates one existing file or directory by its access list,
// for diagnostics (brique-sec doctor). Hard links are not counted here.
func InspectProtectedPath(name string, directory bool) error {
	trusted, err := trustedFor(nil)
	if err != nil {
		return err
	}
	access := uint32(windows.GENERIC_READ)
	if directory {
		access = directoryAccess
	}
	handle, err := openNoFollow(name, access, windows.OPEN_EXISTING, directory)
	if err != nil {
		return errors.New("protected object unavailable")
	}
	defer windows.CloseHandle(handle)
	_, err = inspect(handle, directory, trusted)
	return err
}
