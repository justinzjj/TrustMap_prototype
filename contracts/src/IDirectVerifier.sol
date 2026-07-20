// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

interface IDirectVerifier {
    function verify(uint256 sourceChainId, uint256 sourceHeight, bytes32 sourceBlockHash, bytes calldata proof)
        external
        returns (bytes32 sourceTrustRoot);
}
