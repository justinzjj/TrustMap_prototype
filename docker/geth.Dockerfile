ARG GETH_IMAGE=ethereum/client-go:v1.17.3
FROM ${GETH_IMAGE}

COPY docker/geth-entrypoint.sh /usr/local/bin/trustmap-geth-entrypoint
COPY docker/geth-healthcheck.sh /usr/local/bin/trustmap-geth-healthcheck

RUN chmod 0555 /usr/local/bin/trustmap-geth-entrypoint /usr/local/bin/trustmap-geth-healthcheck

EXPOSE 8545 8546
ENTRYPOINT ["/usr/local/bin/trustmap-geth-entrypoint"]
