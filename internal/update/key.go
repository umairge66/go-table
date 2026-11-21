package update

// PublicKey checks the signature of every release GoTable installs
// (base64 of an Ed25519 public key). Its private half signs releases in the
// release pipeline (cmd/release-sign, the GOTABLE_RELEASE_KEY secret).
// Changing it means copies with the old key stop accepting updates.
const PublicKey = "89H1G+DhBYyhmOg5pRsyuErOkaHUW14sdbNC8HRIngE="
