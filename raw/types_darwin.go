package raw

type Attr struct {
	Ino       uint64
	Size      uint64
	Blocks    uint64
	Atime     uint64
	Mtime     uint64
	Ctime     uint64
	Crtime_    uint64  // OS X
	Atimensec uint32
	Mtimensec uint32
	Ctimensec uint32
	Crtimensec_ uint32 // OS X
	Mode      uint32
	Nlink     uint32
	Owner
	Rdev    uint32
	Flags_   uint32 //  OS X
}


type SetAttrIn struct {
	SetAttrInCommon

	// OS X only
	Bkuptime_    uint64
	Chgtime_     uint64
	Crtime       uint64
	BkuptimeNsec uint32
	ChgtimeNsec  uint32
	CrtimeNsec   uint32
	Flags_       uint32 // see chflags(2)
}
