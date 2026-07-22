SELECT
    deposit_chain AS src_chain,
    withdrawal_chain AS dst_chain,
    bridge_name AS bridge_name,
    deposit_block_number AS src_block_number,
    withdrawal_block_number AS dst_block_number,
    COUNT(*) AS tx_count,
    SUM(amount_usd) AS volume_usd,
    deposit_block_time AS src_block_time,
    withdrawal_block_time AS dst_block_time
FROM bridges_evms.flows
WHERE deposit_chain IS NOT NULL
  AND withdrawal_chain IS NOT NULL
  AND deposit_block_number IS NOT NULL
  AND withdrawal_block_number IS NOT NULL
  AND deposit_block_time IS NOT NULL
  AND deposit_block_time >= TIMESTAMP '2025-12-01 00:00:00 UTC'
  AND deposit_block_time < TIMESTAMP '2026-01-01 00:00:00 UTC'
GROUP BY
    deposit_chain,
    withdrawal_chain,
    bridge_name,
    deposit_block_number,
    withdrawal_block_number,
    deposit_block_time,
    withdrawal_block_time
ORDER BY
    src_block_time,
    src_chain,
    src_block_number,
    dst_chain,
    dst_block_number,
    bridge_name,
    dst_block_time;
