
Objective
=========

A high-performance FUSE API that minimizes pitfalls with writing
correct filesystems.

Decisions
=========

   * Nodes contain references to their children. This is useful
     because most filesystems will need to construct tree-like
     structures.

   * Nodes can be "persistent", meaning their lifetime is not under
     control of the kernel. This is useful for constructing FS trees
     in advance, rather than driven by LOOKUP..

   * The NodeID for FS tree node must be defined on creation and are
     immutable. By contrast, reusing NodeIds (eg. rsc/bazil FUSE, as
     well as old go-fuse/fuse/nodefs) is racy when notify and FORGET
     operations race.
     
   * The mode of an Inode is defined on creation.  Files cannot change
     type during their lifetime. This also prevents the common error
     of forgetting to return the filetype in Lookup/GetAttr.
     
   * The NodeID (used for communicating with kernel) is equal to
     Attr.Ino (value shown in Stat and Lstat return values.). 

   * No global treelock, to ensure scalability.

   * Support for hard links. libfuse doesn't support this in the
     high-level API.  Extra care for race conditions is needed when
    looking up the same file different paths.

   * do not issue Notify{Entry,Delete} as part of
     AddChild/RmChild/MvChild: because NodeIDs are unique and
     immutable, there is no confusion about which nodes are
     invalidated, and the notification doesn't have to happen under
     lock.

   * Directory reading uses the DirStream. Semantics for rewinding
     directory reads, and adding files after opening (but before
     reading) are handled automatically.

To decide
=========

   * Should we provide automatic fileID numbering?
   
   * One giant interface with many methods, or many one-method interfaces?
 
   * one SetAttr method, or many (Chown, Truncate, etc.)

   * function signatures, or types? The latter is easier to remember?
     Easier to extend?

```
    func Lookup(name string, out *EntryOut) (Node, Status) {
    }


    type LookupOp struct {
      // in
      Name string

      // out
      Child Node
      Out *EntryOut
    }
    func Lookup(op LookupOp)
```

   * What to do with semi-unused fields (CreateIn.Umask, OpenIn.Mode, etc.)
   
   * cancellation through context.Context (standard, more GC overhead)
     or a custom context (could reuse across requests.)?

   * Readlink return: []byte or string ?

   * Should Operations.Lookup return *Inode or Operations ?

   * Should bridge.Lookup() add the child, bridge.Unlink remove the child, etc.?


 
