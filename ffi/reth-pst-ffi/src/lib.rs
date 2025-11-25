//! FFI bindings for Reth's Parallel Sparse Trie
//! This exposes a C-compatible interface for use from Go via cgo

#![allow(unsafe_code)]
#![allow(clippy::missing_safety_doc)]

use std::ffi::CStr;
use std::os::raw::{c_char, c_int, c_void};
use std::ptr;
use std::sync::Mutex;

use hex;
use reth_trie_common::Nibbles;
use reth_trie_sparse::provider::{
    DefaultTrieNodeProvider, DefaultTrieNodeProviderFactory, RevealedNode, 
    TrieNodeProvider, TrieNodeProviderFactory
};
use reth_execution_errors::{SparseTrieError, SparseTrieErrorKind};
use reth_trie_sparse::SparseTrieInterface;
use reth_trie_sparse_parallel::{ParallelSparseTrie, ParallelismThresholds};
use alloy_primitives::{Bytes, B256};
use alloy_rlp::{Decodable, Encodable};
use alloy_trie::{TrieAccount, EMPTY_ROOT_HASH};
use std::collections::HashMap;

/// Callback function type for reading nodes from Bor's database
/// Returns: (node_data_ptr, node_data_len, error_code)
/// If node_data_ptr is null, node was not found (error_code should be 0)
/// If error_code != 0, an error occurred
type BorNodeReaderCallback = unsafe extern "C" fn(
    handle_ptr: *mut c_void,    // Opaque handle for registry lookup
    owner_ptr: *const u8,        // 32-byte owner hash
    path_ptr: *const u8,         // path bytes
    path_len: usize,             // path length
    node_data_ptr: *mut *mut u8, // output: pointer to node data (caller must free)
    node_data_len: *mut usize,   // output: node data length
) -> c_int;

/// TrieNodeProvider that reads nodes from Bor's database via callback
/// This matches Reth's pattern where providers hold references to database accessors
struct BorTrieNodeProvider {
    owner: B256,
    reader: BorNodeReaderCallback,
    handle_ptr: *mut c_void, // Handle pointer for registry lookup
}

impl TrieNodeProvider for BorTrieNodeProvider {
    fn trie_node(&self, path: &Nibbles) -> Result<Option<RevealedNode>, SparseTrieError> {
        // Convert nibbles to bytes (path)
        let path_bytes = path.pack();
        
        // Call the Go callback to read the node
        // Note: We need a handle pointer, but we don't have it here
        // The handle should be stored in the factory or passed differently
        // For now, pass null - this will need to be fixed when we wire up the Go side
        let mut node_data_ptr: *mut u8 = std::ptr::null_mut();
        let mut node_data_len: usize = 0;
        
        let result = unsafe {
            (self.reader)(
                self.handle_ptr,
                self.owner.as_slice().as_ptr(),
                path_bytes.as_ptr(),
                path_bytes.len(),
                &mut node_data_ptr,
                &mut node_data_len,
            )
        };
        
        if result != 0 {
            // Error occurred
            return Err(SparseTrieErrorKind::Other(
                format!("Failed to read node from Bor database: error code {}", result).into()
            ).into());
        }
        
        if node_data_ptr.is_null() {
            // Node not found
            return Ok(None);
        }
        
        // Copy the node data
        let node_data = unsafe {
            let slice = std::slice::from_raw_parts(node_data_ptr, node_data_len);
            Bytes::copy_from_slice(slice)
        };
        
        // Free the memory allocated by Go (using C's free)
        unsafe {
            let free_fn: extern "C" fn(*mut c_void) = {
                extern "C" {
                    fn free(ptr: *mut c_void);
                }
                free
            };
            free_fn(node_data_ptr as *mut c_void);
        }
        
        // Return the node (without masks for now - Bor doesn't provide them)
        Ok(Some(RevealedNode {
            node: node_data,
            tree_mask: None,
            hash_mask: None,
        }))
    }
}

/// Factory for creating Bor TrieNodeProviders
/// This matches Reth's TrieNodeProviderFactory pattern for production-safe database access
struct BorTrieNodeProviderFactory {
    account_owner: B256,
    account_reader: BorNodeReaderCallback,
    handle_ptr: *mut c_void, // Handle pointer for registry lookup in Go callback
}

impl TrieNodeProviderFactory for BorTrieNodeProviderFactory {
    type AccountNodeProvider = BorTrieNodeProvider;
    type StorageNodeProvider = BorTrieNodeProvider;

    fn account_node_provider(&self) -> Self::AccountNodeProvider {
        BorTrieNodeProvider {
            owner: self.account_owner,
            reader: self.account_reader,
            handle_ptr: self.handle_ptr,
        }
    }

    fn storage_node_provider(&self, _account: B256) -> Self::StorageNodeProvider {
        // For storage, we use the same owner and reader
        // In a more sophisticated implementation, we might have separate storage readers
        BorTrieNodeProvider {
            owner: self.account_owner,
            reader: self.account_reader,
            handle_ptr: self.handle_ptr,
        }
    }
}

/// Enum-based factory that can be either default or Bor-specific
/// This allows us to store the factory without trait objects
enum ProviderFactory {
    Default(DefaultTrieNodeProviderFactory),
    Bor(BorTrieNodeProviderFactory),
}

/// Enum to represent different provider types
enum Provider {
    Default(DefaultTrieNodeProvider),
    Bor(BorTrieNodeProvider),
}

impl TrieNodeProvider for Provider {
    fn trie_node(&self, path: &Nibbles) -> Result<Option<RevealedNode>, SparseTrieError> {
        match self {
            Provider::Default(p) => p.trie_node(path),
            Provider::Bor(p) => p.trie_node(path),
        }
    }
}

impl ProviderFactory {
    fn create_account_provider(&self) -> Provider {
        match self {
            ProviderFactory::Default(_) => Provider::Default(DefaultTrieNodeProvider),
            ProviderFactory::Bor(factory) => Provider::Bor(factory.account_node_provider()),
        }
    }
    
    fn create_storage_provider(&self, account: B256) -> Provider {
        match self {
            ProviderFactory::Default(_) => Provider::Default(DefaultTrieNodeProvider),
            ProviderFactory::Bor(factory) => Provider::Bor(factory.storage_node_provider(account)),
        }
    }
}

/// SparseStateTrie-like wrapper that manages account trie and storage tries
struct SparseStateTrieWrapper {
    /// Account trie (using ParallelSparseTrie)
    account_trie: ParallelSparseTrie,
    /// Storage tries, one per account address
    storage_tries: HashMap<B256, ParallelSparseTrie>,
    /// Reusable buffer for RLP encoding
    account_rlp_buf: Vec<u8>,
    /// Provider factory for creating node providers on-demand
    /// Uses DefaultTrieNodeProviderFactory if no database is available
    provider_factory: ProviderFactory,
}

impl SparseStateTrieWrapper {
    fn new() -> Self {
        let thresholds = ParallelismThresholds {
            min_revealed_nodes: 100,
            min_updated_nodes: 100,
        };
        Self {
            account_trie: ParallelSparseTrie::default().with_parallelism_thresholds(thresholds),
            storage_tries: HashMap::new(),
            account_rlp_buf: Vec::with_capacity(110), // TRIE_ACCOUNT_RLP_MAX_SIZE
            provider_factory: ProviderFactory::Default(DefaultTrieNodeProviderFactory),
        }
    }
    
    fn new_with_provider(owner: B256, reader: BorNodeReaderCallback, handle_ptr: *mut c_void) -> Self {
        Self {
            account_trie: ParallelSparseTrie::default().with_parallelism_thresholds(ParallelismThresholds {
                min_revealed_nodes: 100,
                min_updated_nodes: 100,
            }),
            storage_tries: HashMap::new(),
            account_rlp_buf: Vec::with_capacity(110),
            provider_factory: ProviderFactory::Bor(BorTrieNodeProviderFactory {
                account_owner: owner,
                account_reader: reader,
                handle_ptr,
            }),
        }
    }

    /// Get or create storage trie for an account
    fn get_or_create_storage_trie(&mut self, address: B256) -> &mut ParallelSparseTrie {
        let thresholds = ParallelismThresholds {
            min_revealed_nodes: 100,
            min_updated_nodes: 100,
        };
        self.storage_tries.entry(address).or_insert_with(|| {
            ParallelSparseTrie::default().with_parallelism_thresholds(thresholds)
        })
    }

    /// Get account value from account trie
    fn get_account_value(&self, address: &B256) -> Option<Vec<u8>> {
        let nibbles = Nibbles::unpack(*address);
        self.account_trie.get_leaf_value(&nibbles).cloned()
    }

    /// Update account with storage root calculation
    fn update_account(
        &mut self,
        address: B256,
        account_rlp: Vec<u8>,
    ) -> Result<bool, ()> {
        
        // Calculate storage root
        let storage_root = if let Some(storage_trie) = self.storage_tries.get_mut(&address) {
            // Storage trie exists, calculate its root
            storage_trie.root()
        } else {
            // No storage trie, try to get storage root from existing account
            if let Some(existing_value) = self.get_account_value(&address) {
                // Decode existing account to get storage root
                TrieAccount::decode(&mut &existing_value[..])
                    .map(|acc| acc.storage_root)
                    .unwrap_or(EMPTY_ROOT_HASH)
            } else {
                // New account, empty storage root
                EMPTY_ROOT_HASH
            }
        };

        // Decode the provided account RLP to check if account is empty
        let account = match TrieAccount::decode(&mut &account_rlp[..]) {
            Ok(acc) => acc,
            Err(_) => return Err(()),
        };

        // Check if account should be removed (empty account with empty storage)
        if account.nonce == 0 
            && account.balance.is_zero() 
            && account.code_hash == EMPTY_ROOT_HASH
            && storage_root == EMPTY_ROOT_HASH {
            // Remove account
            let nibbles = Nibbles::unpack(address);
            let provider = self.provider_factory.create_account_provider();
            self.account_trie.remove_leaf(&nibbles, provider).map_err(|_| ())?;
            return Ok(false);
        }

        // Encode account with storage root
        let mut trie_account = account;
        trie_account.storage_root = storage_root;
        
        self.account_rlp_buf.clear();
        trie_account.encode(&mut self.account_rlp_buf);

        // Update account leaf - create provider on-demand (matches Reth's pattern)
        let nibbles = Nibbles::unpack(address);
        let provider = self.provider_factory.create_account_provider();
        self.account_trie.update_leaf(nibbles, self.account_rlp_buf.clone(), provider)
            .map_err(|_| ())?;

        Ok(true)
    }

    /// Update storage slot
    fn update_storage(
        &mut self,
        address: B256,
        slot_nibbles: Nibbles,
        value: Vec<u8>,
    ) -> Result<(), ()> {
        // Create storage provider on-demand (matches Reth's pattern)
        let provider = self.provider_factory.create_storage_provider(address);
        let storage_trie = self.get_or_create_storage_trie(address);
        
        if value.is_empty() {
            storage_trie.remove_leaf(&slot_nibbles, provider).map_err(|_| ())?;
        } else {
            storage_trie.update_leaf(slot_nibbles, value, provider).map_err(|_| ())?;
        }

        // Update account storage root
        self.update_account_storage_root(address)
    }

    /// Update account storage root after storage changes
    fn update_account_storage_root(&mut self, address: B256) -> Result<(), ()> {
        let provider = self.provider_factory.create_account_provider();
        
        // Get current account value
        let existing_value = match self.get_account_value(&address) {
            Some(v) => v,
            None => return Ok(()), // Account doesn't exist, nothing to update
        };

        // Decode account
        let mut trie_account = TrieAccount::decode(&mut &existing_value[..]).map_err(|_| ())?;

        // Calculate new storage root
        let storage_root = if let Some(storage_trie) = self.storage_tries.get_mut(&address) {
            storage_trie.root()
        } else {
            EMPTY_ROOT_HASH
        };

        // Update storage root
        trie_account.storage_root = storage_root;

        // Check if account should be removed
        if trie_account.nonce == 0 
            && trie_account.balance.is_zero() 
            && trie_account.code_hash == EMPTY_ROOT_HASH
            && storage_root == EMPTY_ROOT_HASH {
            let nibbles = Nibbles::unpack(address);
            self.account_trie.remove_leaf(&nibbles, provider).map_err(|_| ())?;
            return Ok(());
        }

        // Update account with new storage root
        self.account_rlp_buf.clear();
        trie_account.encode(&mut self.account_rlp_buf);
        let nibbles = Nibbles::unpack(address);
        self.account_trie.update_leaf(nibbles, self.account_rlp_buf.clone(), provider)
            .map_err(|_| ())?;

        Ok(())
    }

    /// Remove account
    fn remove_account(&mut self, address: B256) -> Result<(), ()> {
        let provider = self.provider_factory.create_account_provider();
        let nibbles = Nibbles::unpack(address);
        self.account_trie.remove_leaf(&nibbles, provider).map_err(|_| ())?;
        // Note: We keep storage tries even after account removal for simplicity
        // In production, you might want to clean them up
        Ok(())
    }

    /// Get root hash
    fn root(&mut self) -> B256 {
        self.account_trie.root()
    }
}

/// Opaque handle to a SparseStateTrieWrapper instance
pub struct SSTHandle {
    trie: Mutex<SparseStateTrieWrapper>,
}

/// Opaque handle to a ParallelSparseTrie instance (kept for backward compatibility)
pub struct PSTHandle {
    trie: Mutex<ParallelSparseTrie>,
}

/// Create a new SparseStateTrie instance (mimics Reth's SparseStateTrie)
/// Returns a handle pointer, or NULL on error
#[unsafe(no_mangle)]
pub extern "C" fn sst_new() -> *mut SSTHandle {
    let trie = SparseStateTrieWrapper::new();
    let handle = SSTHandle {
        trie: Mutex::new(trie),
    };
    Box::into_raw(Box::new(handle))
}

/// Create a new SparseStateTrie instance with a database provider
/// owner_ptr: 32-byte owner hash (state root for account trie)
/// reader_callback: Function pointer to callback (passed as void*, will be cast to BorNodeReaderCallback)
/// Returns a handle pointer, or NULL on error
/// Note: The handle pointer will be passed to the callback for registry lookup
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_new_with_provider(
    owner_ptr: *const u8,
    reader_callback: *mut c_void,
) -> *mut SSTHandle {
    if owner_ptr.is_null() {
        return std::ptr::null_mut();
    }
    
    if reader_callback.is_null() {
        return std::ptr::null_mut();
    }
    
    let owner = B256::from_slice(unsafe { std::slice::from_raw_parts(owner_ptr, 32) });
    
    // Cast the void* to the callback function pointer type
    let reader: BorNodeReaderCallback = std::mem::transmute(reader_callback);
    
    // Create handle first (we'll pass its pointer to the factory)
    let handle = Box::new(SSTHandle {
        trie: Mutex::new(SparseStateTrieWrapper::new()),
    });
    let handle_ptr = Box::into_raw(handle);
    
    // Now create the wrapper with provider, passing the handle pointer
    let trie = SparseStateTrieWrapper::new_with_provider(owner, reader, handle_ptr as *mut c_void);
    unsafe {
        (*handle_ptr).trie = Mutex::new(trie);
    }
    
    handle_ptr
}

/// Free a SparseStateTrie instance
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_free(handle: *mut SSTHandle) {
    if !handle.is_null() {
        unsafe {
            drop(Box::from_raw(handle));
        }
    }
}

/// Update account in the state trie
/// address: 32-byte hashed address
/// account_rlp: RLP-encoded account (StateAccount)
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_update_account(
    handle: *mut SSTHandle,
    address_ptr: *const u8,
    account_rlp_ptr: *const u8,
    account_rlp_len: usize,
) -> c_int {
    if handle.is_null() || address_ptr.is_null() || account_rlp_ptr.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let address = B256::from_slice(unsafe { std::slice::from_raw_parts(address_ptr, 32) });
    let account_rlp = unsafe { std::slice::from_raw_parts(account_rlp_ptr, account_rlp_len) }.to_vec();

    let mut trie = handle.trie.lock().unwrap();
    match trie.update_account(address, account_rlp) {
        Ok(_) => 0,
        Err(_) => -1,
    }
}

/// Update storage slot
/// address: 32-byte hashed address
/// slot_hex: hex-encoded slot nibbles (without terminator)
/// value: raw bytes (empty for delete)
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_update_storage(
    handle: *mut SSTHandle,
    address_ptr: *const u8,
    slot_hex: *const c_char,
    value_ptr: *const u8,
    value_len: usize,
) -> c_int {
    if handle.is_null() || address_ptr.is_null() || slot_hex.is_null() {
        return -1;
    }
    if value_len > 0 && value_ptr.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let address = B256::from_slice(unsafe { std::slice::from_raw_parts(address_ptr, 32) });
    
    let slot_str = match unsafe { CStr::from_ptr(slot_hex) }.to_str() {
        Ok(s) => s,
        Err(_) => return -1,
    };

    let slot_nibbles = match hex_to_nibbles(slot_str) {
        Ok(n) => n,
        Err(_) => return -1,
    };

    let value = if value_len > 0 {
        unsafe { std::slice::from_raw_parts(value_ptr, value_len) }.to_vec()
    } else {
        Vec::new()
    };

    let mut trie = handle.trie.lock().unwrap();
    match trie.update_storage(address, slot_nibbles, value) {
        Ok(_) => 0,
        Err(_) => -1,
    }
}

/// Remove account
/// address: 32-byte hashed address
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_remove_account(
    handle: *mut SSTHandle,
    address_ptr: *const u8,
) -> c_int {
    if handle.is_null() || address_ptr.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let address = B256::from_slice(unsafe { std::slice::from_raw_parts(address_ptr, 32) });
    let mut trie = handle.trie.lock().unwrap();
    match trie.remove_account(address) {
        Ok(_) => 0,
        Err(_) => -1,
    }
}

/// Get root hash of the state trie
/// Writes 32 bytes to output buffer
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn sst_root(handle: *mut SSTHandle, output: *mut u8) -> c_int {
    if handle.is_null() || output.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let mut trie = handle.trie.lock().unwrap();
    let root = trie.root();
    
    unsafe {
        ptr::copy_nonoverlapping(root.as_slice().as_ptr(), output, 32);
    }
    0
}

/// Create a new ParallelSparseTrie instance (kept for backward compatibility)
/// Returns a handle pointer, or NULL on error
/// The trie is initialized with an empty root node (already revealed by default)
/// Uses Reth's recommended parallelism thresholds for optimal performance
#[unsafe(no_mangle)]
pub extern "C" fn pst_new() -> *mut PSTHandle {
    // ParallelSparseTrie::default() already has an empty root revealed
    // Set parallelism thresholds matching Reth's production values
    let thresholds = ParallelismThresholds {
        min_revealed_nodes: 100,
        min_updated_nodes: 100,
    };
    let trie = ParallelSparseTrie::default()
        .with_parallelism_thresholds(thresholds);
    let handle = PSTHandle {
        trie: Mutex::new(trie),
    };
    Box::into_raw(Box::new(handle))
}

/// Free a ParallelSparseTrie instance
#[unsafe(no_mangle)]
pub unsafe extern "C" fn pst_free(handle: *mut PSTHandle) {
    if !handle.is_null() {
        unsafe {
            drop(Box::from_raw(handle));
        }
    }
}

/// Update a leaf in the trie
/// key: hex-encoded nibbles (without terminator)
/// value: raw bytes
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn pst_update_leaf(
    handle: *mut PSTHandle,
    key_hex: *const c_char,
    value_ptr: *const u8,
    value_len: usize,
) -> c_int {
    if handle.is_null() || key_hex.is_null() {
        return -1;
    }
    // Allow null pointer if value_len is 0 (empty value for delete)
    if value_len > 0 && value_ptr.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let key_str = match unsafe { CStr::from_ptr(key_hex) }.to_str() {
        Ok(s) => s,
        Err(_) => return -1,
    };

    // Convert hex string to nibbles (terminator is already removed in hex_to_nibbles)
    let nibbles = match hex_to_nibbles(key_str) {
        Ok(n) => n,
        Err(_) => return -1,
    };

    // Copy value bytes (handle null pointer for empty values)
    let value = if value_len > 0 {
        unsafe { std::slice::from_raw_parts(value_ptr, value_len) }.to_vec()
    } else {
        Vec::new()
    };
    
    // Use default provider (no-op for blind nodes)
    let provider = DefaultTrieNodeProvider;
    let mut trie = handle.trie.lock().unwrap();
    
    // For empty values (deletes), use remove_leaf instead of update_leaf
    if value.is_empty() {
        match trie.remove_leaf(&nibbles, provider) {
            Ok(_) => 0,
            Err(e) => {
                eprintln!("[Reth FFI] remove_leaf failed: {:?}", e);
                -1
            },
        }
    } else {
        // Use the trait method update_leaf for non-empty values
        match trie.update_leaf(nibbles.clone(), value, provider) {
            Ok(_) => 0,
            Err(e) => {
                eprintln!("[Reth FFI] update_leaf failed: {:?}", e);
                eprintln!("[Reth FFI] Nibbles: len={}, first_10={:?}", 
                    nibbles.len(),
                    nibbles.iter().take(10).collect::<Vec<_>>()
                );
                -1
            },
        }
    }
}

/// Get the root hash of the trie
/// Writes 32 bytes to output buffer
/// Returns 0 on success, -1 on error
#[unsafe(no_mangle)]
pub unsafe extern "C" fn pst_root(handle: *mut PSTHandle, output: *mut u8) -> c_int {
    if handle.is_null() || output.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let mut trie = handle.trie.lock().unwrap();
    let root = trie.root();
    
    // Copy 32 bytes to output
    let root_bytes = root.as_slice();
    unsafe {
        ptr::copy_nonoverlapping(root_bytes.as_ptr(), output, 32);
    }
    0
}

/// Helper function to convert hex string to nibbles
/// The hex string is produced by Go's hex.EncodeToString(nibbles), where each byte (0-15) 
/// is encoded as 2 hex characters. We need to decode it back to bytes first.
/// Note: Go's keybytesToHex includes a terminator byte (0x10 = 16) at the end.
/// Reth's update_leaf expects nibbles WITHOUT the terminator, so we strip it.
fn hex_to_nibbles(hex: &str) -> Result<Nibbles, ()> {
    // Decode hex string to bytes (each byte represents a nibble 0-15, plus terminator 0x10)
    let mut bytes = hex::decode(hex).map_err(|_| ())?;
    // Remove the terminator byte (0x10 = 16) if present - Reth doesn't expect it
    if bytes.last() == Some(&16) {
        bytes.pop();
    }
    // Convert bytes to nibbles (each byte is already a nibble)
    Ok(Nibbles::from_nibbles_unchecked(bytes))
}
