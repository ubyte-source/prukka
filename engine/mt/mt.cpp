// mt is the bundled machine-translation helper: it loads one CTranslate2
// Opus-MT model plus its SentencePiece tokenizers once, then streams
// translations over a line protocol — one UTF-8 source line on stdin yields
// one translated line on stdout. The Go `prukka mt` subcommand owns the JSON
// framing on both sides, so this process stays a pure, warm translator with no
// JSON and no network. No Python, no Perl: SentencePiece is the single
// compiled tokenizer, replacing the Moses+subword-nmt chain the 2019 models
// needed.
#include <ctranslate2/translator.h>
#include <sentencepiece_processor.h>

#include <iostream>
#include <string>
#include <vector>

namespace {

// argValue returns the token after name on argv, or def when absent.
std::string argValue(int argc, char** argv, const std::string& name,
                     const std::string& def) {
  for (int i = 1; i + 1 < argc; ++i) {
    if (name == argv[i]) {
      return argv[i + 1];
    }
  }
  return def;
}

// loadTokenizer aborts with a clear message when a SentencePiece model is
// missing or corrupt — a half-loaded translator would fail silently per line.
void loadTokenizer(sentencepiece::SentencePieceProcessor& sp,
                   const std::string& path) {
  const auto status = sp.Load(path);
  if (!status.ok()) {
    std::cerr << "mt: load " << path << ": " << status.ToString() << "\n";
    std::exit(1);
  }
}

}  // namespace

int main(int argc, char** argv) {
  const std::string modelDir = argValue(argc, argv, "--model", "");
  const std::string sourceSpm = argValue(argc, argv, "--source-spm", "");
  const std::string targetSpm = argValue(argc, argv, "--target-spm", "");
  // targetTag is the >>lang<< token multilingual Opus-MT models expect; it is
  // empty for a single-pair model and then never prepended.
  const std::string targetTag = argValue(argc, argv, "--target-tag", "");
  const int beamSize = std::stoi(argValue(argc, argv, "--beam", "2"));

  if (modelDir.empty() || sourceSpm.empty() || targetSpm.empty()) {
    std::cerr << "usage: mt --model DIR --source-spm FILE --target-spm FILE "
                 "[--target-tag >>eng<<] [--beam N]\n";
    return 2;
  }

  sentencepiece::SentencePieceProcessor sourceTokenizer;
  sentencepiece::SentencePieceProcessor targetTokenizer;
  loadTokenizer(sourceTokenizer, sourceSpm);
  loadTokenizer(targetTokenizer, targetSpm);

  const ctranslate2::models::ModelLoader loader(modelDir);
  ctranslate2::Translator translator(loader);

  ctranslate2::TranslationOptions options;
  options.beam_size = static_cast<size_t>(beamSize);
  options.max_decoding_length = 256;

  // Line-buffered: the pipeline is latency-bound, so flush every translation
  // rather than batching for throughput we do not need.
  std::string line;
  while (std::getline(std::cin, line)) {
    if (line.empty()) {
      std::cout << "\n" << std::flush;
      continue;
    }

    std::vector<std::string> pieces;
    const auto status = sourceTokenizer.Encode(line, &pieces);
    if (!status.ok()) {
      std::cerr << "mt: encode: " << status.ToString() << "\n";
      std::cout << "\n" << std::flush;
      continue;
    }
    // Marian/Opus-MT expects an explicit end-of-source; multilingual models
    // additionally want the target-language tag as the first token.
    if (!targetTag.empty()) {
      pieces.insert(pieces.begin(), targetTag);
    }
    pieces.emplace_back("</s>");

    const std::vector<std::vector<std::string>> batch{pieces};
    const auto results = translator.translate_batch(batch, options);

    std::string translated;
    if (!results.empty() && !results.front().output().empty()) {
      targetTokenizer.Decode(results.front().output(), &translated);
    }
    std::cout << translated << "\n" << std::flush;
  }

  return 0;
}
