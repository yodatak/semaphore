# How to test semaphore-wrapper

```bash
SEMAPHORE_DB_DIALECT=bolt \
SEMAPHORE_CONFIG_PATH=/tmp/semaphore
SEMAPHORE_DB_HOST=/tmp/semaphore \
./semaphore-wrapper ../../../bin/semaphore server --config /tmp/semaphore/config.json
```
