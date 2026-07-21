// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {IDirectVerifier} from "./IDirectVerifier.sol";

/// @notice EXPERIMENTAL direct verifier with a deployment-fixed synthetic gas profile.
/// @dev This contract is a cost simulator, not a PoW SPV verifier, consensus verifier, or
///      light client. Chained hashes and distinct real ECDSA checks approximate a configured
///      verification workload without claiming the security properties of those protocols.
contract ExperimentalCostedDirectVerifier is IDirectVerifier {
    error EmptyAuthorizedSigners();
    error ZeroAuthorizedSigner();
    error DuplicateAuthorizedSigner();
    error InvalidSignatureCheckCount();
    error SignatureChecksExceedSignerCount();
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
        "TrustMapExperimentalCostedDirect(address verifier,address gateway,uint256 homeChainId,uint256 sourceChainId,uint256 sourceHeight,bytes32 sourceBlockHash,bytes32 sourceTrustRoot)"
    );
    uint256 private constant SECP256K1_HALF_ORDER = 0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0;
    uint32 public constant MAX_SIGNATURE_CHECKS = 4096;
    uint32 public constant MAX_HASH_ROUNDS = 16384;

    address[] public authorizedSigners;
    mapping(address signer => bool authorized) public isAuthorizedSigner;
    uint32 public immutable signatureChecks;
    uint32 public immutable hashRounds;
    address private immutable binder;
    address public gateway;

    constructor(address[] memory authorizedSigners_, uint32 signatureChecks_, uint32 hashRounds_) {
        uint256 signerCount = authorizedSigners_.length;
        if (signerCount == 0) revert EmptyAuthorizedSigners();
        if (signatureChecks_ == 0) revert InvalidSignatureCheckCount();
        if (signatureChecks_ > MAX_SIGNATURE_CHECKS) revert TooManySignatureChecks();
        if (signatureChecks_ > signerCount) revert SignatureChecksExceedSignerCount();
        if (hashRounds_ > MAX_HASH_ROUNDS) revert TooManyHashRounds();

        for (uint256 i = 0; i < signerCount; ++i) {
            address signer = authorizedSigners_[i];
            if (signer == address(0)) revert ZeroAuthorizedSigner();
            if (isAuthorizedSigner[signer]) revert DuplicateAuthorizedSigner();
            isAuthorizedSigner[signer] = true;
            authorizedSigners.push(signer);
        }

        signatureChecks = signatureChecks_;
        hashRounds = hashRounds_;
        binder = msg.sender;
    }

    function authorizedSignerCount() external view returns (uint256) {
        return authorizedSigners.length;
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

    /// @notice Computes the fully context-bound digest signed by every checked signer.
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
    /// @dev Signature i must be produced by authorizedSigners[i]. The profile and therefore
    ///      the amount of work is fixed at deployment and cannot be reduced by the submitter.
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
            if (_recover(digest, signatures[i]) != authorizedSigners[i]) revert InvalidSignature();
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
