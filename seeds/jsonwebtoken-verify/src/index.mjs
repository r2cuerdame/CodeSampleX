import jwt from 'jsonwebtoken';

// Always pin `algorithms` on verify. Without it the library will accept
// whatever the token's own header claims, which is the algorithm-confusion
// family of bugs: a token signed with the public key as an HMAC secret, or
// alg:none, verifies against a verifier that did not say what it expects.
// decode() only reads the payload and checks NOTHING — it is not verify().
export function sign(payload, secret, options = {}) {
  return jwt.sign(payload, secret, { algorithm: 'HS256', expiresIn: '1h', ...options });
}

export function verify(token, secret) {
  return jwt.verify(token, secret, { algorithms: ['HS256'] });
}
