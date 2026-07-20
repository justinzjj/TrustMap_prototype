// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

import {IDirectVerifier} from "./IDirectVerifier.sol";
import {PathProofVerifier} from "./PathProofVerifier.sol";
import {TrustRootCommitment} from "./TrustRootCommitment.sol";

/// @notice On-chain request entry point and atomic verification/dependency recorder.
contract TrustMapGateway is TrustRootCommitment, PathProofVerifier {
    struct VerificationRequest {
        address requester;
        uint256 sourceChainId;
        uint256 sourceHeight;
        bytes32 sourceBlockHash;
        bool exists;
        bool resolved;
        bool processing;
    }

    error ZeroDirectVerifier();
    error DirectVerifierMustBeContract();
    error UnknownRequest();
    error RequestAlreadyResolved();
    error SourceBlockHashMismatch();
    error InvalidSourceChainId();
    error InvalidSourceBlockHash();
    error RequestIdCollision();
    error RequestInProgress();

    event VerificationRequested(
        bytes32 indexed requestId,
        address indexed requester,
        uint256 indexed sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        uint256 requesterNonce
    );
    event DirectVerificationSucceeded(
        bytes32 indexed requestId,
        uint256 indexed sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot,
        bytes32 dependencyKey
    );
    event PathVerificationSucceeded(
        bytes32 indexed requestId,
        uint256 indexed sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot,
        uint256 hopCount,
        bytes32 dependencyKey
    );
    event DependencyRecorded(
        bytes32 indexed dependencyKey,
        bytes32 indexed requestId,
        uint256 indexed sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot,
        uint32 leafIndex
    );
    event RequestResolved(
        bytes32 indexed requestId,
        address indexed requester,
        bytes32 indexed dependencyKey,
        bool newDependency,
        bytes32 homeTrustRoot
    );

    IDirectVerifier public immutable directVerifier;
    mapping(bytes32 requestId => VerificationRequest request) private requests;
    mapping(address requester => uint256 nextNonce) public requestNonce;
    mapping(bytes32 dependencyKey => bool recorded) public dependencyRecorded;

    constructor(uint8 depth, IDirectVerifier verifier, bytes32 initialTrustRoot)
        TrustRootCommitment(depth, initialTrustRoot)
        PathProofVerifier(depth)
    {
        if (address(verifier) == address(0)) revert ZeroDirectVerifier();
        if (address(verifier).code.length == 0) revert DirectVerifierMustBeContract();
        directVerifier = verifier;
    }

    function requestVerification(uint256 sourceChainId, uint256 sourceHeight, bytes32 sourceBlockHash)
        external
        returns (bytes32 requestId)
    {
        if (sourceChainId == 0) revert InvalidSourceChainId();
        if (sourceBlockHash == bytes32(0)) revert InvalidSourceBlockHash();
        uint256 nonce = requestNonce[msg.sender]++;
        requestId = computeRequestId(msg.sender, nonce, sourceChainId, sourceHeight, sourceBlockHash);
        if (requests[requestId].exists) revert RequestIdCollision();
        requests[requestId] = VerificationRequest({
            requester: msg.sender,
            sourceChainId: sourceChainId,
            sourceHeight: sourceHeight,
            sourceBlockHash: sourceBlockHash,
            exists: true,
            resolved: false,
            processing: false
        });
        emit VerificationRequested(requestId, msg.sender, sourceChainId, sourceHeight, sourceBlockHash, nonce);
    }

    function computeRequestId(
        address requester,
        uint256 nonce,
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash
    ) public view returns (bytes32) {
        return keccak256(
            abi.encode(block.chainid, address(this), requester, nonce, sourceChainId, sourceHeight, sourceBlockHash)
        );
    }

    function computeDependencyKey(
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes32 sourceTrustRoot
    ) public pure returns (bytes32) {
        return keccak256(abi.encode(sourceChainId, sourceHeight, sourceBlockHash, sourceTrustRoot));
    }

    function requestResolved(bytes32 requestId) external view returns (bool) {
        return requests[requestId].resolved;
    }

    function getRequest(bytes32 requestId) external view returns (VerificationRequest memory) {
        return requests[requestId];
    }

    function verifyDirectAndRecord(bytes32 requestId, bytes calldata proof)
        external
        returns (bytes32 dependencyKey, bool newDependency)
    {
        VerificationRequest storage request = _beginRequest(requestId);
        bytes32 sourceTrustRoot =
            directVerifier.verify(request.sourceChainId, request.sourceHeight, request.sourceBlockHash, proof);
        dependencyKey =
            computeDependencyKey(request.sourceChainId, request.sourceHeight, request.sourceBlockHash, sourceTrustRoot);
        emit DirectVerificationSucceeded(
            requestId,
            request.sourceChainId,
            request.sourceHeight,
            request.sourceBlockHash,
            sourceTrustRoot,
            dependencyKey
        );

        newDependency = _recordDependency(requestId, request, sourceTrustRoot, dependencyKey);
        _resolve(requestId, request, dependencyKey, newDependency);
    }

    function verifyPathAndRecord(
        bytes32 requestId,
        bytes32 baseTrustRoot,
        bytes32[] calldata blockHashes,
        Witness[] calldata witnesses
    ) external returns (bytes32 dependencyKey, bool newDependency) {
        VerificationRequest storage request = _beginRequest(requestId);
        if (blockHashes.length == 0) revert EmptyPath();
        if (blockHashes[0] != request.sourceBlockHash) revert SourceBlockHashMismatch();
        _verifyPath(baseTrustRoot, blockHashes, witnesses);

        dependencyKey =
            computeDependencyKey(request.sourceChainId, request.sourceHeight, request.sourceBlockHash, baseTrustRoot);
        emit PathVerificationSucceeded(
            requestId,
            request.sourceChainId,
            request.sourceHeight,
            request.sourceBlockHash,
            baseTrustRoot,
            blockHashes.length,
            dependencyKey
        );

        newDependency = _recordDependency(requestId, request, baseTrustRoot, dependencyKey);
        _resolve(requestId, request, dependencyKey, newDependency);
    }

    function _recordDependency(
        bytes32 requestId,
        VerificationRequest storage request,
        bytes32 sourceTrustRoot,
        bytes32 dependencyKey
    ) private returns (bool) {
        if (dependencyRecorded[dependencyKey]) return false;

        if (activeTreeBlock != block.number) {
            bytes32 anchorLeaf = paperLeaf(currentTrustRoot, blockhash(block.number - 1));
            _beginBlockTree(requestId, anchorLeaf);
        }

        bytes32 dependencyLeaf = paperLeaf(sourceTrustRoot, request.sourceBlockHash);
        uint32 leafIndex = _appendLeaf(requestId, dependencyLeaf, false);
        dependencyRecorded[dependencyKey] = true;
        emit DependencyRecorded(
            dependencyKey,
            requestId,
            request.sourceChainId,
            request.sourceHeight,
            request.sourceBlockHash,
            sourceTrustRoot,
            leafIndex
        );
        return true;
    }

    function _resolve(bytes32 requestId, VerificationRequest storage request, bytes32 dependencyKey, bool newDependency)
        private
    {
        request.processing = false;
        request.resolved = true;
        emit RequestResolved(requestId, request.requester, dependencyKey, newDependency, currentTrustRoot);
    }

    function _beginRequest(bytes32 requestId) private returns (VerificationRequest storage request) {
        request = requests[requestId];
        if (!request.exists) revert UnknownRequest();
        if (request.resolved) revert RequestAlreadyResolved();
        if (request.processing) revert RequestInProgress();
        request.processing = true;
    }

    function _homeTrustRoot() internal view override returns (bytes32) {
        return currentTrustRoot;
    }
}
