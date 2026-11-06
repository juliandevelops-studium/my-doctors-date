# Password blocklist (SR.04) and Argon2id parameters (SR.01)

## Blocklist

- **File:** `services/user_service/internal/password/blocklist.txt`, embedded into the binary.
- **Source:** `xato-net-10-million-passwords-1000000.txt` of the SecLists project (<https://github.com/danielmiessler/SecLists>, `Passwords/Common-Credentials`, MIT license). Downloaded 2026-10-13.
- **Filter:** only entries with at least 15 characters (the minimum length of SR.04, shorter ones are rejected anyway), lower-cased, de-duplicated, printable, at most 512 characters. 10,908 entries remain.
- **Matching:** the candidate password is compared lower-cased and with surrounding whitespace removed. Also rejected: a password that consists of one repeated character, or of a repeated short block (`passwordpasswordpassword`), and a password that contains the user's e-mail address or the part before the `@` (when at least 6 characters long).
- **Update:** replace the file with a newer filtered list. The unit test `TestBlocklistIsLoaded` fails if the list is empty or lost.

## Argon2id

Parameters (RFC 9106, second recommended option for memory-constrained use, above the OWASP minimum):

| Parameter | Value |
|-|-|
| Memory | 64 MiB (`m=65536`) |
| Iterations | 3 (`t=3`) |
| Parallelism | 2 (`p=2`) |
| Salt | 16 random bytes per password |
| Output | 32 bytes |
| Format | PHC string: `$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>` |

- Verification reads the parameters from the stored string, so parameters can be raised later: a hash with weaker parameters is accepted and re-hashed after a successful login.
- The service limits concurrent hash operations (4) to bound memory use. A password longer than 512 characters is never hashed (SR.04).
