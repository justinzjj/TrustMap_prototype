// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {IDirectVerifier} from "./IDirectVerifier.sol";

/// @notice DEVELOPMENT/EXPERIMENTAL verifier backed by one authorized signer.
/// @dev This is an integration aid, not a production consensus or light-client verifier.
///      It never reads or writes the Gateway's TrustRoot and is callable only by its
///      one-time-bound Gateway. Repeated signatures and hash rounds model a configured
///      direct-verification workload; they do not provide committee or light-client security.
contract ExperimentalAttestationDirectVerifier is IDirectVerifier {
    error ZeroAuthorizedSigner();
    error InvalidSignatureCheckCount();
    error TooManySignatureChecks();
    error TooManyHashRounds();
    error OnlyBinder();
    error ZeroGateway();
    error GatewayMustBeContract();
    error GatewayVerifierQueryFailed();
    error GatewayVerifierMismatch();
    error GatewayAlreadyBound();
    error OnlyGateway();
    error InvalidProofSignatureCount();
    error InvalidSignatureLength();
    error InvalidSignature();

    event GatewayBound(address indexed gateway);

    bytes32 public constant ATTESTATION_TYPEHASH = keccak256(
        "TrustMapExperimentalAttestation(address verifier,address gateway,uint256 homeChainId,uint256 sourceChainId,uint256 sourceHeight,bytes32 sourceBlockHash,bytes32 sourceTrustRoot)"
    );
    uint256 private constant SECP256K1_HALF_ORDER = 0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0;
    uint32 public constant MAX_SIGNATURE_CHECKS = 4096;
    uint32 public constant MAX_HASH_ROUNDS = 16384;

    address public immutable authorizedSigner;
    uint32 public immutable signatureChecks;
    uint32 public immutable hashRounds;
    address private immutable binder;
    address public gateway;

    constructor(address signer, uint32 signatureChecks_, uint32 hashRounds_) {
        if (signer == address(0)) revert ZeroAuthorizedSigner();
        if (signatureChecks_ == 0) revert InvalidSignatureCheckCount();
        if (signatureChecks_ > MAX_SIGNATURE_CHECKS) revert TooManySignatureChecks();
        if (hashRounds_ > MAX_HASH_ROUNDS) revert TooManyHashRounds();
        authorizedSigner = signer;
        signatureChecks = signatureChecks_;
        hashRounds = hashRounds_;
        binder = msg.sender;
    }

    /// @notice One-time deployment wiring; no authority remains after binding.
    function bindGateway(address gateway_) external {
        if (msg.sender != binder) revert OnlyBinder();
        if (gateway != address(0)) revert GatewayAlreadyBound();
        if (gateway_ == address(0)) revert ZeroGateway();
        if (gateway_.code.length == 0) revert GatewayMustBeContract();

        (bool success, bytes memory result) = gateway_.staticcall(abi.encodeWithSignature("directVerifier()"));
        if (!success || result.length != 32) revert GatewayVerifierQueryFailed();
        if (abi.decode(result, (address)) != address(this)) revert GatewayVerifierMismatch();

        gateway = gateway_;
        emit GatewayBound(gateway_);
    }

    function attestationDigest(
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot
    ) public view returns (bytes32) {
        bytes32 workHash = keccak256(
            abi.encode(
                ATTESTATION_TYPEHASH,
                address(this),
                gateway,
                block.chainid,
                sourceChainId,
                sourceHeight,
                sourceBlockHash,
                sourceTrustRoot
            )
        );
        for (uint256 i = 0; i < hashRounds; ++i) {
            workHash = keccak256(abi.encodePacked(workHash, i));
        }
        return keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", workHash));
    }

    /// @param proof ABI encoding of `(bytes32 sourceTrustRoot, bytes[] signatures)`.
    /// @dev Every signature authenticates the same context with the same development signer.
    ///      Repetition deliberately models signature-verification gas and calldata, not quorum.
    function verify(uint256 sourceChainId, uint256 sourceHeight, bytes32 sourceBlockHash, bytes calldata proof)
        external
        view
        returns (bytes32 sourceTrustRoot)
    {
        if (msg.sender != gateway || gateway == address(0)) revert OnlyGateway();

        bytes[] memory signatures;
        (sourceTrustRoot, signatures) = abi.decode(proof, (bytes32, bytes[]));
        if (signatures.length != signatureChecks) revert InvalidProofSignatureCount();
        bytes32 digest = attestationDigest(sourceChainId, sourceHeight, sourceBlockHash, sourceTrustRoot);
        for (uint256 i = 0; i < signatures.length; ++i) {
            if (_recover(digest, signatures[i]) != authorizedSigner) revert InvalidSignature();
        }
    }

    function _recover(bytes32 digest, bytes memory signature) private pure returns (address) {
        if (signature.length != 65) revert InvalidSignatureLength();

        bytes32 r;
        bytes32 s;
        uint8 v;
        assembly ("memory-safe") {
            r := mload(add(signature, 0x20))
            s := mload(add(signature, 0x40))
            v := byte(0, mload(add(signature, 0x60)))
        }
        if (v != 27 && v != 28) revert InvalidSignature();
        if (uint256(s) > SECP256K1_HALF_ORDER) revert InvalidSignature();
        return ecrecover(digest, v, r, s);
    }
}
