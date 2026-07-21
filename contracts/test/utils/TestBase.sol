// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

interface Vm {
    struct Log {
        bytes32[] topics;
        bytes data;
        address emitter;
    }

    function expectRevert(bytes4 revertData) external;
    function expectRevert(bytes calldata revertData) external;
    function roll(uint256 newHeight) external;
    function setBlockhash(uint256 blockNumber, bytes32 blockHash) external;
    function chainId(uint256 newChainId) external;
    function prank(address sender) external;
    function cool(address target) external;
    function addr(uint256 privateKey) external returns (address);
    function sign(uint256 privateKey, bytes32 digest) external returns (uint8 v, bytes32 r, bytes32 s);
    function recordLogs() external;
    function getRecordedLogs() external returns (Log[] memory logs);
}

abstract contract TestBase {
    Vm internal constant vm = Vm(address(uint160(uint256(keccak256("hevm cheat code")))));

    function assertEq(bytes32 actual, bytes32 expected) internal pure {
        require(actual == expected, "bytes32 values differ");
    }

    function assertEq(uint256 actual, uint256 expected) internal pure {
        require(actual == expected, "uint256 values differ");
    }

    function assertEq(bool actual, bool expected) internal pure {
        require(actual == expected, "bool values differ");
    }

    function assertEq(address actual, address expected) internal pure {
        require(actual == expected, "address values differ");
    }

    function assertTrue(bool value) internal pure {
        require(value, "expected true");
    }

    function assertFalse(bool value) internal pure {
        require(!value, "expected false");
    }
}
