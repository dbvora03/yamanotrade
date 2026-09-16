# Yamanote prediction-market contracts

Local prototype only. Start Anvil, then deploy the mock collateral and one clockwise Shibuya series:

```sh
anvil
forge script script/DeployAnvil.s.sol:DeployAnvil --rpc-url http://127.0.0.1:8545 --broadcast
forge test
```

The first round uses a fixture train and starts with the standard Anvil first three accounts as a 2-of-3 oracle quorum. Set the printed contract addresses in `frontend/.env.local`; the UI submits `approve` then `placeRangeBet` through an injected wallet connected to chain `31337`.

This is not production-ready or licensed for real collateral. It uses ODPT-style observation windows only as a prototype settlement input.
