//! The in-process IdP every test in this crate talks to.
//!
//! It implements `HttpFetch` and answers from a table, so no test opens a
//! socket, resolves a name, or needs an identity provider to exist. That is
//! the whole reason `HttpFetch` is a trait (proposal §4.1.3).
//!
//! It also records what it was asked, which is how the token exchange tests
//! assert on the form fields and the `Authorization` header rather than on
//! the fact that a request happened.

use crate::fetch::{FetchError, HttpFetch};
use async_trait::async_trait;
use parking_lot::Mutex;
use std::collections::HashMap;

/// What the fake answers with. `Status` and `Transport` exist so the
/// provider-unavailable paths are exercised, not only the happy ones.
#[derive(Clone)]
pub(crate) enum Reply {
    Body(Vec<u8>),
    Status(u16),
    Transport(&'static str),
    TooLarge,
}

impl Reply {
    pub(crate) fn json(v: &serde_json::Value) -> Self {
        Self::Body(serde_json::to_vec(v).expect("fixture json"))
    }

    fn to_result(&self) -> Result<Vec<u8>, FetchError> {
        match self {
            Self::Body(b) => Ok(b.clone()),
            Self::Status(s) => Err(FetchError::Status(*s)),
            Self::Transport(m) => Err(FetchError::Transport((*m).to_string())),
            Self::TooLarge => Err(FetchError::TooLarge),
        }
    }
}

#[derive(Clone, Debug)]
pub(crate) struct PostCall {
    pub url: String,
    pub form: Vec<(String, String)>,
    pub basic: Option<(String, String)>,
}

#[derive(Default)]
struct Inner {
    gets: HashMap<String, Reply>,
    post: Option<Reply>,
    get_log: Vec<String>,
    post_log: Vec<PostCall>,
}

#[derive(Default)]
pub(crate) struct FakeIdp {
    inner: Mutex<Inner>,
}

impl FakeIdp {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    /// Sets (or replaces) what a GET of `url` answers. Replacing is how the
    /// rotation tests move the provider's key set forward between calls.
    pub(crate) fn set_get(&self, url: &str, reply: Reply) {
        self.inner.lock().gets.insert(url.to_string(), reply);
    }

    pub(crate) fn set_post(&self, reply: Reply) {
        self.inner.lock().post = Some(reply);
    }

    pub(crate) fn get_count(&self, url: &str) -> usize {
        self.inner.lock().get_log.iter().filter(|u| *u == url).count()
    }

    pub(crate) fn post_calls(&self) -> Vec<PostCall> {
        self.inner.lock().post_log.clone()
    }
}

#[async_trait]
impl HttpFetch for FakeIdp {
    async fn get(&self, url: &str) -> Result<Vec<u8>, FetchError> {
        let mut inner = self.inner.lock();
        inner.get_log.push(url.to_string());
        match inner.gets.get(url) {
            Some(reply) => reply.to_result(),
            // An unexpected URL is a test bug worth seeing as a failure
            // rather than as a mysterious 404.
            None => Err(FetchError::Transport(format!("fake has no route for {url}"))),
        }
    }

    async fn post_form(
        &self,
        url: &str,
        form: &[(&str, &str)],
        basic: Option<(&str, &str)>,
    ) -> Result<Vec<u8>, FetchError> {
        let mut inner = self.inner.lock();
        inner.post_log.push(PostCall {
            url: url.to_string(),
            form: form
                .iter()
                .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                .collect(),
            basic: basic.map(|(u, p)| (u.to_string(), p.to_string())),
        });
        match &inner.post {
            Some(reply) => reply.to_result(),
            None => Err(FetchError::Transport("fake has no token response".into())),
        }
    }
}
