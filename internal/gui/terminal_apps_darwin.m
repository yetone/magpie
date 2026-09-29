//go:build darwin

#import <AppKit/AppKit.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdlib.h>
#include <string.h>

// A generic text editor can open a shell script too. Keep only apps whose
// bundle explicitly claims .command or Terminal's shell-script content type.
static BOOL claimsCommandFile(NSBundle *bundle) {
    NSArray *documentTypes = bundle.infoDictionary[@"CFBundleDocumentTypes"];
    if (![documentTypes isKindOfClass:[NSArray class]]) return NO;
    for (NSDictionary *documentType in documentTypes) {
        if (![documentType isKindOfClass:[NSDictionary class]]) continue;
        NSArray *extensions = documentType[@"CFBundleTypeExtensions"];
        if (![extensions isKindOfClass:[NSArray class]]) extensions = @[];
        for (NSString *extension in extensions) {
            if ([extension isKindOfClass:[NSString class]] && [extension caseInsensitiveCompare:@"command"] == NSOrderedSame) {
                return YES;
            }
        }
        NSArray *contentTypes = documentType[@"LSItemContentTypes"];
        if (![contentTypes isKindOfClass:[NSArray class]]) contentTypes = @[];
        for (NSString *contentType in contentTypes) {
            if ([contentType isKindOfClass:[NSString class]] && [contentType isEqualToString:@"com.apple.terminal.shell-script"]) {
                return YES;
            }
        }
    }
    return NO;
}

char *magpieTerminalAppsJSON(void) {
    @autoreleasepool {
        UTType *type = [UTType typeWithFilenameExtension:@"command"];
        if (type == nil) return NULL;
        NSWorkspace *workspace = [NSWorkspace sharedWorkspace];
        NSURL *defaultURL;
        NSArray<NSURL *> *appURLs;
        // magpie needs macOS 12 (LSMinimumSystemVersion); the check only
        // keeps builds aimed at an older default target quiet.
        if (@available(macOS 12.0, *)) {
            defaultURL = [workspace URLForApplicationToOpenContentType:type];
            appURLs = [workspace URLsForApplicationsToOpenContentType:type];
        } else {
            return NULL;
        }
        NSString *defaultID = defaultURL ? [NSBundle bundleWithURL:defaultURL].bundleIdentifier : nil;
        NSMutableArray *apps = [NSMutableArray array];
        NSMutableSet *seen = [NSMutableSet set];
        for (NSURL *appURL in appURLs) {
            NSBundle *bundle = [NSBundle bundleWithURL:appURL];
            NSString *bundleID = bundle.bundleIdentifier;
            if (bundleID.length == 0 || !claimsCommandFile(bundle) || [seen containsObject:bundleID]) continue;
            NSString *name = [bundle objectForInfoDictionaryKey:@"CFBundleDisplayName"];
            if (name.length == 0) name = [bundle objectForInfoDictionaryKey:@"CFBundleName"];
            if (name.length == 0) name = appURL.lastPathComponent.stringByDeletingPathExtension;
            [apps addObject:@{ @"id": bundleID, @"name": name, @"path": appURL.path }];
            [seen addObject:bundleID];
        }
        NSDictionary *result = @{ @"apps": apps, @"default": defaultID ?: @"" };
        NSData *data = [NSJSONSerialization dataWithJSONObject:result options:0 error:NULL];
        if (data == nil) return NULL;
        char *output = malloc(data.length + 1);
        if (output == NULL) return NULL;
        memcpy(output, data.bytes, data.length);
        output[data.length] = '\0';
        return output;
    }
}
